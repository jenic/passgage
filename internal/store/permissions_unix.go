//go:build !windows

package store

import "os"

func restrictFile(f *os.File) error { return f.Chmod(0600) }
