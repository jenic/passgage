package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/internal/cli"
	"github.com/jenic/passgage/internal/store"
)

func setup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PASSAGE_DIR", filepath.Join(dir, "store"))
	t.Setenv("PASSAGE_IDENTITIES_FILE", filepath.Join(dir, "identities"))
	t.Setenv("PASSAGE_RECIPIENTS", "")
	t.Setenv("PASSAGE_RECIPIENTS_FILE", "")
	t.Setenv("PASSAGE_AGE", "")
	c, e := cfg.Load()
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(c, ""); e != nil {
		t.Fatal(e)
	}
}
func run(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	var out, errout bytes.Buffer
	c := cli.New(strings.NewReader(input), &out, &errout)
	c.SetArgs(cli.NormalizeArgs(args))
	e := c.Execute()
	return out.String(), e
}
func TestCoreWorkflowWithoutTools(t *testing.T) {
	setup(t)
	t.Setenv("PATH", t.TempDir())
	if _, e := run(t, "one\nmetadata\n\n", "insert", "-m", "mail/work"); e != nil {
		t.Fatal(e)
	}
	b, e := run(t, "", "mail/work")
	if e != nil || b != "one\nmetadata\n\n" {
		t.Fatalf("implicit show: %q %v", b, e)
	}
	if _, e = run(t, "", "generate", "-n", "-i", "mail/work", "30"); e != nil {
		t.Fatal(e)
	}
	b, e = run(t, "", "show", "mail/work")
	if e != nil || len(strings.Split(b, "\n")[0]) != 30 || !strings.HasSuffix(b, "\nmetadata\n\n") {
		t.Fatalf("in-place: %q %v", b, e)
	}
	if _, e = run(t, "", "cp", "mail/work", "other"); e != nil {
		t.Fatal(e)
	}
	if _, e = run(t, "", "mv", "other", "renamed"); e != nil {
		t.Fatal(e)
	}
	if _, e = run(t, "", "reencrypt"); e != nil {
		t.Fatal(e)
	}
	b, e = run(t, "", "grep", "-n", "metadata")
	if e != nil || !strings.Contains(b, "mail/work:2:metadata") {
		t.Fatalf("grep: %q %v", b, e)
	}
	if _, e = run(t, "", "rm", "-f", "renamed"); e != nil {
		t.Fatal(e)
	}
}
func TestNoninteractiveOverwriteRefused(t *testing.T) {
	setup(t)
	run(t, "one", "insert", "-m", "a")
	if _, e := run(t, "two", "insert", "-m", "a"); e == nil {
		t.Fatal("overwrite accepted")
	}
	b, _ := run(t, "", "a")
	if b != "one" {
		t.Fatal("original changed")
	}
}
func TestHelpWithoutKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PASSAGE_DIR", filepath.Join(dir, "missing"))
	t.Setenv("PASSAGE_IDENTITIES_FILE", filepath.Join(dir, "missing-key"))
	for _, args := range [][]string{{"--help"}, {"version"}, {"completion", "powershell"}} {
		if _, e := run(t, "", args...); e != nil {
			t.Fatal(e)
		}
	}
}
func TestListWithoutIdentity(t *testing.T) {
	setup(t)
	run(t, "one", "insert", "-m", "entry")
	if e := os.Remove(os.Getenv("PASSAGE_IDENTITIES_FILE")); e != nil {
		t.Fatal(e)
	}
	b, e := run(t, "", "ls")
	if e != nil || b != "Passgage\n└── entry\n" {
		t.Fatalf("%q %v", b, e)
	}
}

func TestTreeListingsWithoutTools(t *testing.T) {
	setup(t)
	t.Setenv("PATH", t.TempDir())
	if got, err := run(t, ""); err != nil || got != "Passgage\n" {
		t.Fatalf("empty store: %q %v", got, err)
	}
	for _, name := range []string{"email/work", "email/personal", "server", "server/login"} {
		if _, err := run(t, "secret", "insert", "-m", name); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := run(t, "", "show", "server"); err != nil || got != "secret" {
		t.Fatalf("entry must take precedence over directory: %q %v", got, err)
	}
	root := os.Getenv("PASSAGE_DIR")
	for _, name := range []string{"empty", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ordinary.txt", ".hidden/entry.age"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("ignored"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Windows may require additional privileges for symlinks.
	if err := os.Symlink(filepath.Join(root, "email"), filepath.Join(root, "linked")); err != nil {
		t.Logf("symlink fixture unavailable: %v", err)
	}
	if err := os.Remove(os.Getenv("PASSAGE_IDENTITIES_FILE")); err != nil {
		t.Fatal(err)
	}
	want := "Passgage\n├── email\n│   ├── personal\n│   └── work\n├── server\n│   └── login\n└── server\n"
	for _, args := range [][]string{nil, {"show"}, {"ls"}, {"list"}} {
		if got, err := run(t, "", args...); err != nil || got != want {
			t.Fatalf("%v: %q %v", args, got, err)
		}
	}
	for _, args := range [][]string{{"email"}, {"show", "email/"}, {"ls", "email"}, {"list", "email"}} {
		if got, err := run(t, "", args...); err != nil || got != "email\n├── personal\n└── work\n" {
			t.Fatalf("%v: %q %v", args, got, err)
		}
	}
	if got, err := run(t, "", "show", "empty"); err != nil || got != "empty\n" {
		t.Fatalf("empty: %q %v", got, err)
	}
	if got, err := run(t, "", "find", "email"); err != nil || got != "email/personal\nemail/work\n" {
		t.Fatalf("find changed: %q %v", got, err)
	}
	for _, flag := range []string{"--clip", "--qrcode"} {
		if _, err := run(t, "", "show", flag, "email"); err == nil {
			t.Fatalf("directory accepted %s", flag)
		}
	}
}

func TestSingleEntrySuffixAndFailedEncryption(t *testing.T) {
	setup(t)
	if _, e := run(t, "first", "insert", "-m", "nested/entry/"); e != nil {
		t.Fatal(e)
	}
	if b, e := run(t, "", "nested/entry"); e != nil || b != "first" {
		t.Fatalf("normalized entry: %q %v", b, e)
	}
	t.Setenv("PASSAGE_RECIPIENTS", "invalid")
	if _, e := run(t, "replacement", "insert", "-m", "-f", "nested/entry"); e == nil || strings.Contains(e.Error(), "some files were changed") {
		t.Fatalf("misreported failed encryption: %v", e)
	}
}

func TestAttachedQRLine(t *testing.T) {
	setup(t)
	if _, e := run(t, "first\nsecond\n", "insert", "-m", "entry"); e != nil {
		t.Fatal(e)
	}
	if b, e := run(t, "", "show", "-q2", "entry"); e != nil || len(b) == 0 {
		t.Fatalf("attached line flag: %v", e)
	}
	got := cli.NormalizeArgs([]string{"show", "-c3", "entry", "--", "-c4"})
	if got[1] != "-c=3" || got[4] != "-c4" {
		t.Fatalf("normalization: %v", got)
	}
	message := cli.NormalizeArgs([]string{"git", "commit", "-m", "-c3"})
	if message[3] != "-c3" {
		t.Fatal("changed Git commit message")
	}
}
