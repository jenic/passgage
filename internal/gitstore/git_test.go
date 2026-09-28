package gitstore

import (
	"bytes"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
)

func TestAutomaticCommitPreservesStaging(t *testing.T) {
	fs := memfs.New()
	r, e := git.Init(memory.NewStorage(), fs)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := r.Config()
	c.User.Name = "Test"
	c.User.Email = "test@example.invalid"
	r.SetConfig(c)
	write := func(p, s string) {
		f, e := fs.Create(p)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte(s))
		f.Close()
	}
	write("a.age", "first")
	var out bytes.Buffer
	if e = CommitPaths(r, "store", []string{"a.age"}, "first", &out); e != nil {
		t.Fatal(e)
	}
	write("unrelated", "staged")
	w, _ := r.Worktree()
	w.Add("unrelated")
	write("a.age", "second")
	if e = CommitPaths(r, "store", []string{"a.age"}, "second", &out); e != nil {
		t.Fatal(e)
	}
	h, _ := r.Head()
	commit, _ := r.CommitObject(h.Hash())
	tree, _ := commit.Tree()
	if _, e = tree.File("unrelated"); e == nil {
		t.Fatal("included unrelated staging")
	}
	idx, _ := r.Storer.Index()
	if _, e = idx.Entry("unrelated"); e != nil {
		t.Fatal("lost unrelated staging")
	}
	if !bytes.Contains(out.Bytes(), []byte("Committing store: second")) {
		t.Fatal("missing custom diagnostic")
	}
}
func TestExternalTransportRejected(t *testing.T) {
	for _, u := range []string{"/tmp/repo", "file:///tmp/repo", "git://example.com/repo", "https://user:secret@example.com/repo"} {
		if _, e := endpoint(u); e == nil {
			t.Errorf("accepted %s", u)
		}
	}
}
