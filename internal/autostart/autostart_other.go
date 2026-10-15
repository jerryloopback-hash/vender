//go:build !linux && !windows

package autostart

import (
	"fmt"
	"runtime"
)

// On a platform vender does not ship a start-at-login mechanism for, the entry
// points fail loudly instead of silently doing nothing. vender itself targets
// Linux and Windows 11; this file exists so the package still compiles for
// anything else (a `GOOS=darwin go build ./...` check, say).
var errUnsupported = fmt.Errorf("autostart: no start-at-login mechanism for %s/%s", runtime.GOOS, runtime.GOARCH)

// Enable registers vender to start the daemon at login. It is idempotent.
func Enable() error { return errUnsupported }

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return errUnsupported }

// Status reports the current autostart registration.
func Status() (State, error) { return State{Mechanism: MechanismUnknown}, errUnsupported }
