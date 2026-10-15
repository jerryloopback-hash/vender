//go:build windows

package autostart

// Enable registers vender to start the daemon at logon. It is idempotent.
func Enable() error {
	_, err := defaultWindowsBackend().enable()
	return err
}

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return defaultWindowsBackend().disable() }

// Status reports the current autostart registration.
func Status() (State, error) { return defaultWindowsBackend().status() }
