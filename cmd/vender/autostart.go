package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/autostart"
)

// Seams for the CLI tests. Autostart writes to the host's real start-at-login
// configuration (a systemd unit, a desktop entry, a registry value), so a test
// must never reach the real implementation through the command; it substitutes
// a recorder here. The package's own tests cover the real logic against an
// isolated HOME.
var (
	autostartEnable  = autostart.Enable
	autostartDisable = autostart.Disable
	autostartStatus  = autostart.Status
)

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

Neither needs administrator rights. ` + "`on`" + ` and ` + "`off`" + ` are idempotent, and
` + "`status`" + ` exits non-zero when nothing is registered.`,
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

	var (
		st  autostart.State
		err error
	)
	switch action {
	case "on":
		err = autostartEnable()
		if err == nil {
			st, err = autostartStatus()
		}
	case "off":
		err = autostartDisable()
		if err == nil {
			st, err = autostartStatus()
		}
	default:
		st, err = autostartStatus()
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
