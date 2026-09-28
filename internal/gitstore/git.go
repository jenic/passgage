// Package gitstore provides native Git operations without invoking Git programs.
package gitstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
)

func init() {
	// In particular, go-git's file transport invokes git-upload-pack. Disable it
	// even if an unexpected URL rewrite bypasses our command-level validation.
	for _, scheme := range []string{"file", "git", "http"} {
		client.InstallProtocol(scheme, nil)
	}
}

// Open opens only the repository rooted at the store, never an ancestor.
func Open(dir string) (*git.Repository, error) { return git.PlainOpen(dir) }

func worktree(r *git.Repository) (*git.Worktree, error) {
	w, err := r.Worktree()
	if err == nil {
		w.Excludes = append(w.Excludes, gitignore.ParsePattern(".passgage.lock", nil), gitignore.ParsePattern(".passgage-*", nil))
	}
	return w, err
}

// AutoCommit commits only affected files, preserving unrelated staging.
func AutoCommit(dir string, paths []string, message string, out io.Writer) error {
	r, err := Open(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return nil
	}
	if err != nil {
		return err
	}
	return CommitPaths(r, dir, paths, message, out)
}

// CommitPaths isolates automatic commits from unrelated index changes.
func CommitPaths(r *git.Repository, dir string, paths []string, message string, out io.Writer) (result error) {
	if len(paths) == 0 {
		return nil
	}
	if err := checkConfig(r); err != nil {
		return err
	}
	original, err := r.Storer.Index()
	if err != nil {
		return err
	}
	baseline := &index.Index{Version: 2}
	ref, err := r.Head()
	if err == nil {
		commit, e := r.CommitObject(ref.Hash())
		if e != nil {
			return e
		}
		tree, e := commit.Tree()
		if e != nil {
			return e
		}
		e = tree.Files().ForEach(func(f *object.File) error {
			baseline.Entries = append(baseline.Entries, &index.Entry{Name: f.Name, Hash: f.Hash, Mode: f.Mode})
			return nil
		})
		if e != nil {
			return e
		}
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return err
	}
	if err = r.Storer.SetIndex(baseline); err != nil {
		return err
	}
	committed := false
	defer func() {
		restore := original
		if committed {
			now, e := r.Storer.Index()
			if e != nil {
				result = errors.Join(result, e)
				return
			}
			affected := map[string]bool{}
			for _, p := range paths {
				affected[p] = true
			}
			restore = &index.Index{Version: 2}
			for _, entry := range original.Entries {
				if !affected[entry.Name] {
					restore.Entries = append(restore.Entries, entry)
				}
			}
			for _, entry := range now.Entries {
				if affected[entry.Name] {
					restore.Entries = append(restore.Entries, entry)
				}
			}
		}
		result = errors.Join(result, r.Storer.SetIndex(restore))
	}()
	w, err := worktree(r)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if _, err = w.Filesystem.Stat(p); os.IsNotExist(err) {
			idx, e := r.Storer.Index()
			if e != nil {
				return e
			}
			_, _ = idx.Remove(p)
			if e = r.Storer.SetIndex(idx); e != nil {
				return e
			}
		} else if err != nil {
			return err
		} else {
			if _, err = w.Add(p); err != nil {
				return err
			}
		}
	}
	// Compare the staged tree to HEAD, ignoring unrelated worktree changes.
	status, err := w.Status()
	if err != nil {
		return err
	}
	changed := false
	for _, p := range paths {
		if f, ok := status[p]; ok && f.Staging != git.Unmodified && f.Staging != git.Untracked {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	fmt.Fprintf(out, "Committing %s: %s\n", dir, message)
	_, err = w.Commit(message, &git.CommitOptions{})
	if err != nil {
		return err
	}
	committed = true
	return nil
}
func checkConfig(r *git.Repository) error {
	c, err := r.ConfigScoped(config.GlobalScope)
	if err != nil {
		return err
	}
	if strings.EqualFold(c.Raw.Section("commit").Option("gpgsign"), "true") {
		return fmt.Errorf("Git commit signing is configured but external signing programs are unsupported")
	}
	for _, s := range c.Raw.Sections {
		if s.Name == "filter" && len(s.Subsections) > 0 {
			return fmt.Errorf("Git filters are configured; native filtering is unsupported")
		}
	}
	return nil
}

// Local executes the documented local Git command subset.
func Local(r *git.Repository, dir string, args []string, out io.Writer, decrypt func([]byte) ([]byte, error)) error {
	if len(args) == 0 {
		return fmt.Errorf("expected a Git subcommand")
	}
	w, err := worktree(r)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: git status")
		}
		s, e := w.Status()
		if e == nil {
			fmt.Fprint(out, s.String())
		}
		return e
	case "log":
		if len(args) != 1 {
			return fmt.Errorf("usage: git log")
		}
		it, e := r.Log(&git.LogOptions{})
		if e != nil {
			return e
		}
		defer it.Close()
		return it.ForEach(func(c *object.Commit) error {
			_, e := fmt.Fprintf(out, "%s %s\n", c.Hash.String(), strings.TrimSpace(c.Message))
			return e
		})
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: git add <path>...")
		}
		for _, p := range args[1:] {
			if !safeGitPath(p) {
				return fmt.Errorf("invalid Git path")
			}
			if _, e := w.Add(p); e != nil {
				return e
			}
		}
		return nil
	case "commit":
		if len(args) != 3 || args[1] != "-m" {
			return fmt.Errorf("usage: git commit -m <message>")
		}
		if e := checkConfig(r); e != nil {
			return e
		}
		fmt.Fprintf(out, "Committing %s: %s\n", dir, args[2])
		_, e := w.Commit(args[2], &git.CommitOptions{})
		return e
	case "config":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: git config <user.name|user.email> [value]")
		}
		if args[1] != "user.name" && args[1] != "user.email" {
			return fmt.Errorf("only user.name and user.email are supported")
		}
		c, e := r.Config()
		if e != nil {
			return e
		}
		if len(args) == 3 {
			if args[1] == "user.name" {
				c.User.Name = args[2]
			} else {
				c.User.Email = args[2]
			}
			return r.SetConfig(c)
		}
		c, e = r.ConfigScoped(config.GlobalScope)
		if e != nil {
			return e
		}
		if args[1] == "user.name" {
			fmt.Fprintln(out, c.User.Name)
		} else {
			fmt.Fprintln(out, c.User.Email)
		}
		return nil
	case "remote":
		return remote(r, args[1:], out)
	case "diff":
		plain := len(args) == 2 && args[1] == "--decrypt"
		if len(args) > 1 && !plain {
			return fmt.Errorf("usage: git diff [--decrypt]")
		}
		return diff(r, w, plain, out, decrypt)
	default:
		return fmt.Errorf("unsupported Git command %q", args[0])
	}
}
func safeGitPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.ContainsAny(p, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return true
}
func remote(r *git.Repository, args []string, out io.Writer) error {
	if len(args) == 0 {
		rs, e := r.Remotes()
		if e != nil {
			return e
		}
		for _, remote := range rs {
			c := remote.Config()
			fmt.Fprintf(out, "%s\t%s\n", c.Name, strings.Join(c.URLs, " "))
		}
		return nil
	}
	if len(args) == 3 && args[0] == "add" {
		if _, e := endpoint(args[2]); e != nil {
			return e
		}
		_, e := r.CreateRemote(&config.RemoteConfig{Name: args[1], URLs: []string{args[2]}})
		return e
	}
	if len(args) == 2 && args[0] == "remove" {
		return r.DeleteRemote(args[1])
	}
	if len(args) == 3 && args[0] == "set-url" {
		if _, e := endpoint(args[2]); e != nil {
			return e
		}
		c, e := r.Config()
		if e != nil {
			return e
		}
		v, ok := c.Remotes[args[1]]
		if !ok {
			return fmt.Errorf("unknown remote")
		}
		v.URLs = []string{args[2]}
		return r.SetConfig(c)
	}
	return fmt.Errorf("usage: git remote [add <name> <url>|remove <name>|set-url <name> <url>]")
}
func diff(r *git.Repository, w *git.Worktree, plain bool, out io.Writer, decrypt func([]byte) ([]byte, error)) error {
	status, err := w.Status()
	if err != nil {
		return err
	}
	var names []string
	for n := range status {
		names = append(names, n)
	}
	sort.Strings(names)
	var tree *object.Tree
	if h, e := r.Head(); e == nil {
		c, e := r.CommitObject(h.Hash())
		if e != nil {
			return e
		}
		tree, e = c.Tree()
		if e != nil {
			return e
		}
	}
	for _, n := range names {
		if !plain || !strings.HasSuffix(n, ".age") {
			fmt.Fprintf(out, "%c%c %s (binary or metadata)\n", status[n].Staging, status[n].Worktree, n)
			continue
		}
		var old, new []byte
		if tree != nil {
			f, e := tree.File(n)
			if e == nil {
				b, e := f.Contents()
				if e != nil {
					return e
				}
				old, e = decrypt([]byte(b))
				if e != nil {
					return e
				}
			}
		}
		f, e := w.Filesystem.Open(n)
		if e == nil {
			b, e := io.ReadAll(f)
			_ = f.Close()
			if e != nil {
				return e
			}
			new, e = decrypt(b)
			if e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		fmt.Fprintf(out, "--- a/%s\n+++ b/%s\n", n, n)
		for _, l := range strings.Split(string(old), "\n") {
			fmt.Fprintln(out, "-"+l)
		}
		for _, l := range strings.Split(string(new), "\n") {
			fmt.Fprintln(out, "+"+l)
		}
		clear(old)
		clear(new)
	}
	return nil
}
