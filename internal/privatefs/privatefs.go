// Package privatefs protects vender's credential and control directories.
package privatefs

import "os"

func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return Protect(path)
}
