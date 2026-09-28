package gitstore

import (
	"io"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"
)

func TestNativeSyncAndDivergence(t *testing.T) {
	t.Setenv("PASSGAGE_GIT_TOKEN", "")
	remote, e := git.Init(memory.NewStorage(), memfs.New())
	if e != nil {
		t.Fatal(e)
	}
	identify := func(r *git.Repository) {
		c, _ := r.Config()
		c.User.Name = "Test"
		c.User.Email = "test@example.invalid"
		if e := r.SetConfig(c); e != nil {
			t.Fatal(e)
		}
	}
	identify(remote)
	commit := func(r *git.Repository, name, value string) {
		w, _ := r.Worktree()
		f, e := w.Filesystem.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte(value))
		f.Close()
		if e = CommitPaths(r, "memory", []string{name}, value, io.Discard); e != nil {
			t.Fatal(e)
		}
	}
	commit(remote, "a.age", "initial")
	original := client.Protocols["https"]
	client.InstallProtocol("https", server.NewServer(server.MapLoader{"https://passgage.invalid/store": remote.Storer}))
	t.Cleanup(func() { client.InstallProtocol("https", original) })
	local, e := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{URL: "https://passgage.invalid/store"})
	if e != nil {
		t.Fatal(e)
	}
	identify(local)
	commit(remote, "a.age", "updated")
	if e = Network(local, []string{"pull"}, nil); e != nil {
		t.Fatal(e)
	}
	lh, _ := local.Head()
	rh, _ := remote.Head()
	if lh.Hash() != rh.Hash() {
		t.Fatal("fast-forward failed")
	}
	commit(local, "local.age", "local")
	if e = Network(local, []string{"push"}, nil); e != nil {
		t.Fatal(e)
	}
	// The remote worktree is not updated by a receive-pack; update it to its HEAD.
	w, _ := remote.Worktree()
	if e = w.Reset(&git.ResetOptions{Mode: git.HardReset}); e != nil {
		t.Fatal(e)
	}
	commit(local, "local.age", "diverged local")
	commit(remote, "a.age", "diverged remote")
	before, _ := local.Head()
	if e = Network(local, []string{"pull"}, nil); e == nil {
		t.Fatal("divergence accepted")
	}
	after, _ := local.Head()
	if before.Hash() != after.Hash() {
		t.Fatal("rejected pull moved HEAD")
	}
	if e = Network(local, []string{"push"}, nil); e == nil {
		t.Fatal("non-fast-forward push accepted")
	}
	f, _ := local.Worktree()
	dirty, _ := f.Filesystem.Create("dirty.age")
	dirty.Write([]byte("uncommitted"))
	dirty.Close()
	if e = Network(local, []string{"pull"}, nil); e == nil {
		t.Fatal("dirty pull accepted")
	}
}
