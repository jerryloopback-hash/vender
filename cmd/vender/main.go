// Command vender is the CLI entry point for the vender proxy client.
//
//	vender version              print build information
//	vender import <link>        add a server from a vless:// or hysteria2:// link
//	vender servers list|rm      inspect and edit servers.yaml
//	vender daemon|status|...    (M1) engine control, (M3) TUI, (M4) autostart
package main

import (
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// cobra has already printed the message; only the exit code is ours.
		os.Exit(1)
	}
}
