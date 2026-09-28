//go:build !windows

package editor

import "os"

func secure(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if fi.IsDir() {
		mode = 0700
	}
	return os.Chmod(p, mode)
}
