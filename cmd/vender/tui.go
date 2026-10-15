package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/tui"
)

func (a *app) newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Attach the interactive terminal UI",
		Long: `Start the Bubble Tea terminal UI and attach it to the running daemon.

The TUI is a client: it reads the same configuration files as the CLI and sends
control requests (switch server, change mode, start/stop the engine, follow
logs, watch traffic) to the daemon over its local control channel. Because the
daemon is authoritative, the TUI can attach and detach at any time, and two of
them can be open at once.

It does not need a daemon to start. Without one it shows how to start it, and
Nodes and Settings keep working from the configuration files on disk.

  Dashboard  status, live up/down traffic, latency        1
  Nodes      configured servers, select one               2
  Logs       the engine's own log, follow and filter      3
  Settings   routing mode, listener, the built-in policy  4

  tab / shift+tab  switch screens        ?  help        q  quit`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			store, err := a.configStore()
			if err != nil {
				return err
			}
			runDir, err := a.runDirPath()
			if err != nil {
				return err
			}
			// Deliberately no signal.NotifyContext here. Bubble Tea installs
			// its own SIGINT/SIGTERM handlers, turns them into a clean quit,
			// and restores the terminal *before* Run returns; cancelling the
			// context on the same signal as well races with that (Bubble Tea's
			// signal goroutine does a blocking send to a channel whose reader
			// may already have left), and the process can hang on the way out
			// with the terminal still in raw mode. One owner of the signal,
			// and it is the component holding the terminal.
			return tui.Run(context.Background(), tui.Options{ConfigDir: store.Dir(), RunDir: runDir})
		},
	}
}
