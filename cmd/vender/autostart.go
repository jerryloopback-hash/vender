package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/autostart"
	"github.com/ll-lite/vender/internal/core"
)

// Seams for the CLI tests. Autostart writes to the host's real start-at-login
// configuration (a systemd unit, a desktop entry, a registry value), so a test
// must never reach the real implementation through the command; it substitutes
// a recorder here. The package's own tests cover the real logic against an
// isolated HOME.
var (
	autostartEnable  = autostart.EnableWithOptions
	autostartDisable = autostart.Disable
	autostartStatus  = autostart.StatusWithOptions
)

// autostartOptions reads what the registration has to deliver. It is a seam for
// the same reason the three entry points are: the CLI tests must not read (or
// write) the real config.yaml.
var autostartOptions = defaultAutostartOptions

// defaultAutostartOptions reads config.yaml and the engine's privilege verdict,
// which is what decides between the per-user mechanism and the TUN-capable one.
// A broken configuration is not fatal here: autostart is about starting the
// daemon, and the daemon reports its own problems.
func defaultAutostartOptions(a *app) autostart.Options {
	var opts autostart.Options
	store, err := a.configStore()
	if err != nil {
		return opts
	}
	opts.ConfigDir = store.Dir()
	opts.RunDir, _ = a.runDirPath()
	cfg, err := store.LoadConfig()
	if err != nil {
		return opts
	}
	opts.TUN = cfg.Engine.Tun.Enabled
	if !opts.TUN {
		return opts
	}
	engine, _ := core.ResolveBinary(*cfg)
	verdict := core.CheckTUNPrivilege(engine)
	if verdict.OK {
		opts.CapabilityHint = "this environment already can create a TUN device (" + verdict.Detail + ")"
		return opts
	}
	opts.CapabilityHint = verdict.Detail
	if verdict.Hint != "" {
		opts.CapabilityHint += "; " + strings.Join(strings.Fields(verdict.Hint), " ")
	}
	return opts
}

func nonEmptyPath(p string) string {
	if p == "" {
		return "<path-to-sing-box>"
	}
	return p
}

// errAutostartDisabled makes `autostart status` exit non-zero when nothing is
// registered, so the command is usable in a script — the same contract as
// `vender status`.
var errAutostartDisabled = errors.New("vender is not registered to start at login (run `vender autostart on`)")

func (a *app) newAutostartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autostart",
		Short: "Start vender automatically at login",
		Long: `Register or inspect vender's start-at-login entry.

The entry only has to run ` + "`vender daemon`" + `, which is headless: no console,
no terminal, one daemon per run directory, and a clean exit on SIGTERM. The
mechanism is chosen per platform:

  Linux    a systemd user unit (vender.service, WantedBy=default.target); an
           XDG autostart desktop entry when no systemd user manager answers
  Windows  a Task Scheduler logon task (hidden, least privilege), falling back
           to the per-user Registry Run key when the task cannot be created

None of that needs administrator rights — unless TUN mode is enabled in
config.yaml. A systemd user unit cannot be given CAP_NET_ADMIN and a least
privilege logon task cannot create a WinTun adapter, so with TUN on ` + "`on`" + ` uses
the mechanism that works:

  Linux    a system unit (User=<you>, AmbientCapabilities=CAP_NET_ADMIN
           CAP_NET_RAW) — one ` + "`sudo vender autostart on`" + ` to install
  Windows  the same logon task registered as HighestAvailable — one elevated
           ` + "`vender autostart on`" + `

If that privileged registration cannot be made, ` + "`on`" + ` still installs the
per-user one and returns an error saying what TUN mode is still missing; either
way ` + "`status`" + ` reports whether what is registered can actually create a TUN.
` + "`on`" + ` and ` + "`off`" + ` are idempotent, and ` + "`status`" + ` exits non-zero when nothing is
registered.`,
	}

	var jsonOut bool
	for _, sub := range []struct {
		use   string
		short string
		run   func(*cobra.Command) error
	}{
		{"on", "Register vender to start at login", func(c *cobra.Command) error {
			return a.runAutostart(c, "on")
		}},
		{"off", "Remove the autostart registration", func(c *cobra.Command) error {
			return a.runAutostart(c, "off")
		}},
		{"status", "Show the autostart registration", func(c *cobra.Command) error {
			return a.runAutostart(c, "status")
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

// autostartJSON is the shared --json object: the state plus, for status, the
// exit code's meaning.
type autostartJSON struct {
	autostart.State
	Action string `json:"action"`
}

func (a *app) runAutostart(cmd *cobra.Command, action string) error {
	out := cmd.OutOrStdout()
	jsonOut, _ := cmd.Flags().GetBool("json")

	opts := autostartOptions(a)

	var (
		st  autostart.State
		err error
	)
	switch action {
	case "on":
		err = autostartEnable(opts)
		if err == nil {
			st, err = autostartStatus(opts)
		}
	case "off":
		err = autostartDisable()
		if err == nil {
			st, err = autostartStatus(opts)
		}
	default:
		st, err = autostartStatus(opts)
	}
	if err != nil {
		return fmt.Errorf("autostart %s: %w", action, err)
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(autostartJSON{State: st, Action: action}); encErr != nil {
			return encErr
		}
	} else {
		printAutostart(out, action, st)
	}

	if action == "on" && err != nil {
		// `on` reports the state it did manage to register before the error, so
		// a TUN registration that fell back to the user unit is not silent.
		if st.Enabled {
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(autostartJSON{State: st, Action: action}); encErr != nil {
					return encErr
				}
			} else {
				printAutostart(out, action, st)
			}
		}
		return fmt.Errorf("autostart %s: %w", action, err)
	}
	if action == "status" && !st.Enabled {
		return errAutostartDisabled
	}
	return nil
}

func printAutostart(out io.Writer, action string, st autostart.State) {
	verb := map[string]string{
		"on":     "registered vender to start at login",
		"off":    "autostart registration removed",
		"status": "autostart status",
	}[action]

	fmt.Fprintf(out, "%s\n", verb)
	fmt.Fprintf(out, "state        %s\n", enabledLabel(st.Enabled))
	fmt.Fprintf(out, "mechanism    %s\n", st.Mechanism)
	if st.Target != "" {
		fmt.Fprintf(out, "target       %s\n", st.Target)
	}
	if st.Detail != "" {
		fmt.Fprintf(out, "detail       %s\n", st.Detail)
	}
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
