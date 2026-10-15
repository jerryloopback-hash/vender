package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build information, injected with -ldflags "-X main.version=..." (see
// scripts/build.sh). Unset values mean "built from a working tree".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func (a *app) newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print vender's build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s %s (commit %s, built %s)\n", appName, version, commit, date)
			fmt.Fprintf(out, "%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
