package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/tui"
)

// milestoneError is the uniform "this command exists but its milestone has not
// landed" error. Every stub returns one so scripts can rely on the CLI surface
// while the exit code still signals that nothing happened.
func milestoneError(what, milestone string) error {
	return fmt.Errorf("%s: not implemented in this milestone (%s)", what, milestone)
}

func (a *app) newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Attach the interactive terminal UI (M3)",
		Long: `Start the Bubble Tea terminal UI and attach it to the running daemon.

The TUI is a client: it reads the same configuration files and sends control
requests to the daemon, so it can be attached and detached at any time.

Not implemented in this milestone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := a.configStore()
			if err != nil {
				return err
			}
			return tui.Run(context.Background(), tui.Options{ConfigDir: store.Dir()})
		},
	}
}

func (a *app) newAutostartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autostart",
		Short: "Start vender automatically at login (M4)",
		Long: `Register or inspect vender's start-at-login entry.

  Linux    a systemd user unit, or an XDG autostart desktop entry
  Windows  the per-user Registry Run key, or a Task Scheduler entry`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "on",
			Short: "Register vender to start at login (M4)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return milestoneError("autostart on", "M4")
			},
		},
		&cobra.Command{
			Use:   "off",
			Short: "Remove the autostart registration (M4)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return milestoneError("autostart off", "M4")
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show the autostart registration (M4)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return milestoneError("autostart status", "M4")
			},
		},
	)
	return cmd
}
