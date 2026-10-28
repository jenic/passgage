//go:build !windows

package storefs

import "os"

// RestrictFile restricts an open file to the current user (and SYSTEM on Windows).
func RestrictFile(f *os.File) error { return f.Chmod(0600) }
