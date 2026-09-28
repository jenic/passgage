// Package testutil provides isolated fixtures for tests.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// GitConfig gives a test its own global go-git configuration. The pinned
// go-git loader reads the first existing global config, with XDG first.
// An actual empty file is required: an empty directory would fall back to
// the runner's home config. This never changes HOME or repository metadata.
func GitConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, "git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}
