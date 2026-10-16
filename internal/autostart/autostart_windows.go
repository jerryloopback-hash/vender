//go:build windows

package autostart

// Enable registers vender to start the daemon at logon. It is idempotent.
func Enable() error { return EnableWithOptions(Options{}) }

// EnableWithOptions registers vender with the mechanism the Options call for:
// a least-privilege logon task by default, the same task with the highest
// available privileges when TUN mode needs it.
func EnableWithOptions(o Options) error {
	_, err := defaultWindowsBackend().enableWith(o)
	return err
}

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return defaultWindowsBackend().disable() }

// Status reports the current autostart registration.
func Status() (State, error) { return StatusWithOptions(Options{}) }

// StatusWithOptions reports the registration *and* whether it can deliver what
// the current configuration needs, which is the only honest answer once TUN
// mode is in play.
func StatusWithOptions(o Options) (State, error) {
	return defaultWindowsBackend().statusWith(o)
}
