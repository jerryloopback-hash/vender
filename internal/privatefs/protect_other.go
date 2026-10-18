//go:build !windows

package privatefs

// Existing POSIX write paths enforce their individual file modes already.
func Protect(path string) error { return nil }
