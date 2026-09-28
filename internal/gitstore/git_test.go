package gitstore

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/jenic/passgage/internal/testutil"
)

func TestAutomaticCommitPreservesStaging(t *testing.T) {
	testutil.GitConfig(t, "")
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

func TestConfigGuards(t *testing.T) {
	for _, scope := range []string{"global", "local"} {
		for _, tc := range []struct{ name, contents, message string }{
			{"filter", "[filter \"lfs\"]\nclean = git-lfs clean -- %f\nrequired = true\n", "Git filters are configured"},
			{"signing", "[commit]\ngpgsign = true\n", "Git commit signing is configured"},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				global := ""
				if scope == "global" {
					global = tc.contents
				}
				testutil.GitConfig(t, global)
				r, err := git.Init(memory.NewStorage(), memfs.New())
				if err != nil {
					t.Fatal(err)
				}
				if scope == "local" {
					c, err := r.Config()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.Unmarshal([]byte(tc.contents)); err != nil {
						t.Fatal(err)
					}
					if err := r.SetConfig(c); err != nil {
						t.Fatal(err)
					}
				}
				if err := checkConfig(r); err == nil || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("expected %q, got %v", tc.message, err)
				}
			})
		}
	}
}

func TestGitConfigIsolation(t *testing.T) {
	// Model an inherited runner config, then isolate a nested test from it.
	testutil.GitConfig(t, "[filter \"runner\"]\nclean = unsupported-filter\n")
	check := func(t *testing.T, wantError bool) {
		t.Helper()
		r, err := git.Init(memory.NewStorage(), memfs.New())
		if err != nil {
			t.Fatal(err)
		}
		err = checkConfig(r)
		if (err != nil) != wantError {
			t.Fatalf("configuration error = %v, want error %v", err, wantError)
		}
	}
	check(t, true)
	t.Run("isolated", func(t *testing.T) {
		testutil.GitConfig(t, "")
		check(t, false)
	})
	check(t, true)
}
func TestExternalTransportRejected(t *testing.T) {
	for _, u := range []string{"/tmp/repo", "file:///tmp/repo", "git://example.com/repo", "https://user:secret@example.com/repo"} {
		if _, e := endpoint(u); e == nil {
			t.Errorf("accepted %s", u)
		}
	}
}
