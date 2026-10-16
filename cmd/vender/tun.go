package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
	"github.com/ll-lite/vender/internal/core"
)

// errTunOff makes `vender tun status` exit non-zero when the TUN inbound is
// off, so the command is usable in a script — the same contract as
// `vender status` and `vender autostart status`.
var errTunOff = errors.New("TUN mode is off (turn it on with `vender tun on`)")

// tunCallTimeout bounds the control call a `tun on|off` makes. Enabling TUN
// restarts the engine, which stops and starts sing-box, so it is generous.
const tunCallTimeout = 60 * time.Second

func (a *app) newTunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tun",
		Short: "Turn transparent, system-wide TUN mode on or off",
		Long: `Control the optional TUN inbound: transparent capture of the host's traffic.

With TUN mode on, vender's single outbound carries every connection the host
makes, not only the applications that were pointed at 127.0.0.1:7897. The mixed
SOCKS5+HTTP inbound stays up, so both ways to use vender work at once.

It is off by default, and it is the only vender feature that needs a kernel
privilege:

  Linux    CAP_NET_ADMIN, on the process or (with setcap) on the sing-box
           binary; a systemd *user* unit cannot have it, which is why
           ` + "`vender autostart on`" + ` installs a *system* unit when TUN is enabled
  Windows  an elevated (Administrator) process, because the WinTun adapter is a
           driver-level device

` + "`tun on`" + ` records the setting in config.yaml and, when a daemon is running,
asks it to re-render and restart the engine; if the engine refuses, the setting
is rolled back rather than left in a state that cannot start. ` + "`tun status`" + `
exits non-zero when TUN is off.

Turning TUN on routes the whole host through the tunnel. If the engine cannot
reach the server, the host loses connectivity (which is why ` + "`strict_route`" + ` is
off by default, and why vender never turns TUN on by itself).`,
	}

	var jsonOut bool
	for _, sub := range []struct {
		use   string
		short string
		run   func(*cobra.Command) error
	}{
		{"on", "Enable TUN mode and restart the engine", func(c *cobra.Command) error {
			return a.setTun(c, true)
		}},
		{"off", "Disable TUN mode and restart the engine", func(c *cobra.Command) error {
			return a.setTun(c, false)
		}},
		{"status", "Show the TUN setting and whether it can work here", func(c *cobra.Command) error {
			return a.tunStatus(c)
		}},
	} {
		s := sub
		c := &cobra.Command{
			Use:   s.use,
			Short: s.short,
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return s.run(cmd) },
		}
		c.Flags().BoolVar(&jsonOut, "json", false, "print the resulting state as JSON")
		cmd.AddCommand(c)
	}
	return cmd
}

// tunJSON is the --json object: the setting, the verdict, and the action.
type tunJSON struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"`
	Stack   string `json:"stack"`
	Iface   string `json:"interface"`
	MTU     int    `json:"mtu"`
	// AutoRoute/StrictRoute are the settings in effect.
	AutoRoute   bool `json:"autoRoute"`
	StrictRoute bool `json:"strictRoute"`
	// PrivilegesOK reports whether *this* process could create the TUN,
	// PrivilegeDetail explains it.
	PrivilegesOK    bool   `json:"privilegesOk"`
	PrivilegeDetail string `json:"privilegeDetail,omitempty"`
	// Daemon reports whether a control call answered (-1 unknown, 0 not
	// running, 1 answered) and DaemonTun is its own view of the TUN state.
	Daemon    int    `json:"daemon"`
	DaemonTun bool   `json:"daemonTun,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (a *app) setTun(cmd *cobra.Command, on bool) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	action := "off"
	if on {
		action = "on"
	}

	store, err := a.configStore()
	if err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}

	rep := a.newTunReport(action, cfg, jsonOut)

	if cfg.Engine.Tun.Enabled == on {
		return a.reportTun(cmd, rep)
	}

	// Prefer the daemon when one is running: it re-renders and restarts the
	// engine itself, and it refuses *before* stopping a working proxy if it
	// cannot create a TUN.
	if running, _, _ := daemonRunningFor(a); running {
		return a.setTunViaDaemon(cmd, rep, on)
	}

	cfg.Engine.Tun.Enabled = on
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("tun %s: %w", action, err)
	}
	if err := store.SaveConfig(cfg); err != nil {
		return fmt.Errorf("tun %s: %w", action, err)
	}
	return a.reportTun(cmd, rep)
}

// tunReport is everything `tun on|off|status` needs to print: the setting, the
// environment's privilege verdict, and how the change was applied.
type tunReport struct {
	action  string
	cfg     *config.Config
	verdict core.TUNPrivilege
	// engine is the resolved sing-box path ("" when it could not be resolved,
	// in which case engineErr says why — TUN cannot work without an engine at
	// all, so that has to be visible).
	engine    string
	engineErr error
	detail    string
	jsonOut   bool
}

func (a *app) newTunReport(action string, cfg *config.Config, jsonOut bool) tunReport {
	engine, engineErr := core.ResolveBinary(*cfg)
	return tunReport{
		action:    action,
		cfg:       cfg,
		verdict:   core.CheckTUNPrivilege(engine),
		engine:    engine,
		engineErr: engineErr,
		jsonOut:   jsonOut,
	}
}

// setTunViaDaemon applies the change through the control channel. A failed
// enable is rolled back — including restarting the engine that the failed
// restart stopped — so the daemon is never left down by a TUN attempt.
func (a *app) setTunViaDaemon(cmd *cobra.Command, rep tunReport, on bool) error {
	action, cfg, jsonOut := rep.action, rep.cfg, rep.jsonOut
	runDir, err := a.runDirPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), tunCallTimeout)
	defer cancel()

	cli, err := control.Dial(ctx, runDir)
	if err != nil {
		return fmt.Errorf("tun %s: %w", action, err)
	}
	defer cli.Close()

	applied, err := cli.SetTun(ctx, on)
	if err != nil {
		detail := ""
		switch {
		case isRefusal(err):
			// The daemon declined before it touched the engine (a missing
			// capability, a bad config), so there is nothing to roll back.
			detail = "nothing was changed; the engine keeps running"
		case on:
			if _, rerr := cli.SetTun(ctx, false); rerr != nil {
				detail = fmt.Sprintf("the setting could not be rolled back (%v)", rerr)
			} else if _, serr := cli.StartEngine(ctx); serr != nil {
				detail = fmt.Sprintf("the engine could not be restarted after the rollback (%v)", serr)
			} else {
				detail = "the setting was rolled back and the engine was restarted"
			}
		}
		if jsonOut {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(tunJSON{
				Enabled: cfg.Engine.Tun.Enabled, Action: action, Stack: cfg.Engine.Tun.Stack,
				PrivilegesOK: rep.verdict.OK, PrivilegeDetail: rep.verdict.Detail,
				Detail: detail, Error: err.Error(),
			})
		}
		if detail != "" {
			return fmt.Errorf("tun %s: %w (%s)", action, err, detail)
		}
		return fmt.Errorf("tun %s: %w", action, err)
	}

	// The daemon persisted the change; re-read so the report shows what is on
	// disk rather than what we hoped.
	if fresh, lerr := a.configStore(); lerr == nil {
		if c, cerr := fresh.LoadConfig(); cerr == nil {
			cfg = c
		}
	}
	rep.cfg = cfg
	rep.detail = fmt.Sprintf("the running daemon applied it (engine healthy: %v)", applied.Status.EngineHealthy)
	return a.reportTun(cmd, rep)
}

// isRefusal reports whether the daemon rejected the request before doing
// anything (bad_request), as opposed to failing part way through a restart.
func isRefusal(err error) bool {
	var perr *control.Error
	return errors.As(err, &perr) && perr.Kind == control.KindBadRequest
}

func (a *app) tunStatus(cmd *cobra.Command) error {
	jsonOut, _ := cmd.Flags().GetBool("json")

	store, err := a.configStore()
	if err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	if err := a.reportTun(cmd, a.newTunReport("status", cfg, jsonOut)); err != nil {
		return err
	}
	if !cfg.Engine.Tun.Enabled {
		return errTunOff
	}
	return nil
}

// reportTun prints the TUN state: the setting, the privilege verdict, and what
// a running daemon thinks.
func (a *app) reportTun(cmd *cobra.Command, rep tunReport) error {
	out := cmd.OutOrStdout()
	action, jsonOut := rep.action, rep.jsonOut
	verdict, detail := rep.verdict, rep.detail
	t := rep.cfg.Engine.Tun

	daemonState, daemonTun, daemonErr := a.daemonTunState(cmd)

	if jsonOut {
		obj := tunJSON{
			Enabled:         t.Enabled,
			Action:          action,
			Stack:           t.Stack,
			Iface:           t.InterfaceName,
			MTU:             t.MTU,
			AutoRoute:       t.AutoRouteEnabled(),
			StrictRoute:     t.StrictRoute,
			PrivilegesOK:    verdict.OK,
			PrivilegeDetail: verdict.Detail,
			Daemon:          daemonState,
			DaemonTun:       daemonTun,
			Detail:          detail,
		}
		if daemonErr != nil {
			obj.Detail = joinDetail(obj.Detail, daemonErr.Error())
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(obj)
	}

	verb := map[string]string{
		"on":     "TUN mode enabled",
		"off":    "TUN mode disabled",
		"status": "TUN mode",
	}[action]
	fmt.Fprintf(out, "%s: %s\n", verb, onOffWord(t.Enabled))
	for _, line := range t.Describe() {
		fmt.Fprintf(out, "  %s\n", line)
	}
	switch {
	case rep.engine != "":
		fmt.Fprintf(out, "  engine             %s\n", rep.engine)
	case rep.engineErr != nil:
		fmt.Fprintf(out, "  engine             NOT RESOLVED — %v\n", rep.engineErr)
	}
	if detail != "" {
		fmt.Fprintf(out, "  %s\n", detail)
	}

	// The privilege verdict is the difference between "on" and "on and
	// working", so it is never omitted. A running daemon may be a system unit
	// that holds CAP_NET_ADMIN, in which case this invocation's own (missing)
	// capability is irrelevant and reporting MISSING would be a lie.
	switch {
	case !t.Enabled:
		fmt.Fprintln(out, "  privileges         not needed while TUN is off")
	case rep.engineErr != nil:
		fmt.Fprintln(out, "  privileges         unknown — no sing-box to inspect")
	case verdict.OK:
		fmt.Fprintf(out, "  privileges         ok — %s\n", verdict.Detail)
	case daemonState == 1 && daemonTun:
		fmt.Fprintln(out, "  privileges         ok — provided by the running daemon, which already has TUN up")
	default:
		fmt.Fprintf(out, "  privileges         MISSING — %s\n", verdict.Detail)
		for _, line := range splitLines(verdict.Hint) {
			fmt.Fprintf(out, "                       %s\n", line)
		}
	}

	switch daemonState {
	case 1:
		fmt.Fprintf(out, "  daemon             attached; its engine has TUN %s\n", onOffWord(daemonTun))
		if !daemonTun && t.Enabled {
			fmt.Fprintln(out, "                       the engine has not been restarted with TUN yet")
		}
	case 0:
		fmt.Fprintln(out, "  daemon             not running (the setting applies at the next start)")
	default:
		if daemonErr != nil {
			fmt.Fprintf(out, "  daemon             unknown (%v)\n", daemonErr)
		}
	}
	return nil
}

// daemonTunState asks a running daemon for its own TUN state. It returns 1 when
// a daemon answered, 0 when none is running, and -1 when the answer is unknown.
func (a *app) daemonTunState(cmd *cobra.Command) (int, bool, error) {
	runDir, err := a.runDirPath()
	if err != nil {
		return -1, false, err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	cli, err := control.Dial(ctx, runDir)
	if err != nil {
		if control.IsNotRunning(err) {
			return 0, false, nil
		}
		return -1, false, err
	}
	defer cli.Close()
	st, err := cli.Status(ctx)
	if err != nil {
		return -1, false, err
	}
	return 1, st.Tun.Active, nil
}

func joinDetail(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
