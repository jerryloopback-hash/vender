//go:build !linux && !windows

package core

import "runtime"

// checkTUNPrivilege is the honest answer on a platform vender does not ship a
// TUN path for: vender targets Linux and Windows 11, and this file exists so
// that `GOOS=darwin go build ./...` still works.
func checkTUNPrivilege(string) TUNPrivilege {
	return TUNPrivilege{
		OK:     false,
		Detail: "TUN mode is not implemented for " + runtime.GOOS,
	}
}
