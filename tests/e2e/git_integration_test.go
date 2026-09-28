//go:build integration

package e2e

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/jenic/passgage/internal/gitstore"
)

// This opt-in test writes .git only in a disposable temporary directory.
// It is for CI/user execution, not the restricted development workspace.
func TestFilesystemGit(t *testing.T) {
	dir := t.TempDir()
	r, e := git.PlainInit(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := r.Config()
	c.User.Name = "Test"
	c.User.Email = "test@example.invalid"
	if e = r.SetConfig(c); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "entry.age"), []byte("encrypted fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = gitstore.AutoCommit(dir, []string{"entry.age"}, "test", io.Discard); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Head(); e != nil {
		t.Fatal(e)
	}
}
