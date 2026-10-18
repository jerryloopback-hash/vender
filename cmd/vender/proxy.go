package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *app) newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Turn the desktop's system proxy on or off",
		Long: `Point the operating system's proxy settings at vender's local inbound.

This is a convenience for applications that honour the desktop proxy settings
but have no proxy option of their own. It only rewrites the *user's* proxy
configuration — it never enables a VPN or TUN mode, and it never changes the
listen address or the daemon's state.

  Linux    GNOME: gsettings org.gnome.system.proxy
  Windows  WinINET LAN settings, with a durable per-user backup

On Windows, ` + "`vender proxy off`" + ` restores the saved manual/PAC/auto-detect
settings; it refuses to overwrite external changes unless you explicitly use
` + "`proxy off --force`" + `. On GNOME, off sets mode to none. vender's own
documentation always prefers per-application proxy configuration (or the
http_proxy / https_proxy environment variables printed by ` + "`proxy on`" + `).`,
	}
	off := &cobra.Command{
		Use:   "off",
		Short: "Restore the saved Windows proxy, or disable the GNOME proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			force, _ := cmd.Flags().GetBool("force")
			if force {
				if err := forceRestoreSystemProxy(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Saved system proxy settings restored (forced)")
				return nil
			}
			return a.setProxy(cmd, false)
		},
	}
	off.Flags().Bool("force", false, "Windows: restore saved settings even if another application changed the proxy")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "on",
			Short: "Set the system proxy to vender's local inbound",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.setProxy(cmd, true)
			},
		},
		off,
	)
	return cmd
}

// applySystemProxy is a seam for tests: the real implementation shells out to
// gsettings (Linux), which must never happen during `go test`. Tests replace it
// with a recorder; production code leaves it alone.
var applySystemProxy = setSystemProxy

func (a *app) setProxy(cmd *cobra.Command, on bool) error {
	out := cmd.OutOrStdout()

	if !systemProxySupported() {
		action := "off"
		if on {
			action = "on"
		}
		return fmt.Errorf("proxy %s: system proxy control is not implemented on %s in this milestone (M4)",
			action, runtime.GOOS)
	}

	listen := "127.0.0.1:7897"
	if on || runtime.GOOS != "windows" {
		store, err := a.configStore()
		if err != nil {
			return err
		}
		cfg, err := store.LoadConfig()
		if err != nil {
			return err
		}
		listen = cfg.Proxy.Listen
	}

	if err := applySystemProxy(on, listen); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}

	if on {
		fmt.Fprintf(out, "System proxy set to %s\n", listen)
		if running, _, err := daemonRunningFor(a); err == nil && !running {
			fmt.Fprintf(out, "  warning: no daemon is running, so nothing will answer on %s yet\n", listen)
			fmt.Fprintf(out, "  start it with `vender daemon`\n")
		}
		fmt.Fprintln(out, "For shells and tools that ignore the desktop settings:")
		fmt.Fprintf(out, "  export http_proxy=http://%s\n", listen)
		fmt.Fprintf(out, "  export https_proxy=http://%s\n", listen)
		fmt.Fprintf(out, "  export all_proxy=socks5://%s\n", listen)
		fmt.Fprintln(out, "  export no_proxy=localhost,127.0.0.1,::1")
		return nil
	}

	if runtime.GOOS == "windows" {
		fmt.Fprintln(out, "Saved system proxy settings restored (or no vender backup was present)")
	} else {
		fmt.Fprintln(out, "System proxy disabled (mode: none)")
	}
	fmt.Fprintln(out, "Shells that exported the variables above need to unset them:")
	for _, v := range []string{"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		fmt.Fprintf(out, "  unset %s\n", v)
	}
	return nil
}

// daemonRunningFor resolves the run dir for the proxy command's warnings.
func daemonRunningFor(a *app) (bool, int, error) {
	runDir, err := a.runDirPath()
	if err != nil {
		return false, 0, err
	}
	return daemonRunning(runDir)
}
