package cli_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jenic/passgage/internal/cli"
	"github.com/spf13/cobra"
)

func completionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PASSAGE_DIR", root)
	t.Setenv("PASSAGE_IDENTITIES_FILE", filepath.Join(root, "missing-identity"))
	t.Setenv("PASSWORD_STORE_GENERATED_LENGTH", "25")
	t.Setenv("PASSWORD_STORE_CLIP_TIME", "45")
	t.Setenv("PATH", t.TempDir())
	for _, dir := range []string{"email", "empty", "server", "email/nested", ".git", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"email/work", "email/personal", "email/nested/login", "server", "server/login", "space entry", "日本語", "quote'entry", "literal $value;name", ".hidden/secret", ".git/metadata"} {
		if err := os.WriteFile(filepath.Join(root, name+".age"), []byte("invalid ciphertext: must never decrypt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func complete(t *testing.T, request string, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	var out, errout bytes.Buffer
	c := cli.New(strings.NewReader(""), &out, &errout)
	c.SetArgs(cli.NormalizeArgs(append([]string{request}, args...)))
	if err := c.Execute(); err != nil {
		t.Fatalf("completion %q: %v, %s", args, err, errout.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		t.Fatalf("invalid completion output %q", out.String())
	}
	directive, err := strconv.Atoi(last[1:])
	if err != nil {
		t.Fatal(err)
	}
	var candidates []string
	for _, line := range lines[:len(lines)-1] {
		name, _, _ := strings.Cut(line, "\t")
		candidates = append(candidates, name)
	}
	return candidates, cobra.ShellCompDirective(directive)
}

func TestPathCompletions(t *testing.T) {
	root := completionFixture(t)
	before := snapshot(t, root)
	for _, request := range []string{"__complete", "__completeNoDesc"} {
		for _, command := range []string{"", "show", "ls", "list", "insert", "add", "edit", "generate", "rm", "remove", "delete", "cp", "copy", "mv", "move", "rename"} {
			t.Run(request+"/"+command, func(t *testing.T) {
				args := []string{"email"}
				if command != "" {
					args = append([]string{command}, args...)
				}
				got, directive := complete(t, request, args...)
				want := []string{"email/", "email/nested/", "email/nested/login", "email/personal", "email/work"}
				if !reflect.DeepEqual(got, want) || directive != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace {
					t.Fatalf("got %q, %v; want %q and NoFileComp|NoSpace", got, directive, want)
				}
			})
		}
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"cp", "email/work", "server"}, []string{"server", "server/", "server/login"}},
		{[]string{"move", "email/work", "empty"}, []string{"empty/"}},
		{[]string{"reencrypt", "--path", "email"}, []string{"email/", "email/nested/"}},
		{[]string{"reencrypt", "-p", "empty"}, []string{"empty/"}},
		{[]string{"reencrypt", "--path=email/"}, []string{"email/", "email/nested/"}},
		{[]string{"--clip=2", "email/w"}, []string{"email/work"}},
		{[]string{"show", "-c2", "email/w"}, []string{"email/work"}},
		{[]string{"show", "--", "email/w"}, []string{"email/work"}},
		{[]string{"--allow-age-plugin=yubikey", "email/w"}, []string{"email/work"}},
		{[]string{"space"}, []string{"space entry"}},
		{[]string{"quote"}, []string{"quote'entry"}},
		{[]string{"literal"}, []string{"literal $value;name"}},
		{[]string{"日本"}, []string{"日本語"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got, directive := complete(t, "__completeNoDesc", tc.args...)
			if !reflect.DeepEqual(got, tc.want) || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
				t.Fatalf("got %q, %v; want %q", got, directive, tc.want)
			}
			if !strings.HasSuffix(tc.want[0], "/") && len(tc.want) == 1 && directive&cobra.ShellCompDirectiveNoSpace != 0 {
				t.Fatalf("terminal entry unexpectedly suppresses space: %v", directive)
			}
		})
	}
	if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("completion modified the store")
	}
}

func TestCompletionArgumentLimits(t *testing.T) {
	completionFixture(t)
	for _, args := range [][]string{
		{"email/work", "email"}, {"show", "email/work", "email"},
		{"insert", "entry", "email"}, {"edit", "entry", "email"}, {"rm", "entry", "email"},
		{"generate", "entry", "email"}, {"generate", "entry", "25", "email"},
		{"cp", "source", "destination", "email"}, {"mv", "source", "destination", "email"},
		{"init", "email"}, {"version", "email"}, {"reencrypt", "email"},
		{"find", "email"}, {"search", "email"}, {"grep", "email"}, {"git", "email"},
		{"completion", "zsh", "email"}, {"--allow-age-plugin", "email"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got, directive := complete(t, "__complete", args...)
			if len(got) != 0 || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
				t.Fatalf("unexpected suggestions %q, directive %v", got, directive)
			}
		})
	}
	got, _ := complete(t, "__complete", "")
	for _, want := range []string{"show", "generate", "completion", "email/", "email/work"} {
		if !slices.Contains(got, want) {
			t.Errorf("root completion missing %q: %q", want, got)
		}
	}
	got, _ = complete(t, "__complete", "show", "--cl")
	if !slices.Contains(got, "--clip") {
		t.Fatalf("missing flag completion: %q", got)
	}
}

func TestCompletionWithoutStoreAndOnErrors(t *testing.T) {
	root := completionFixture(t)
	t.Setenv("PASSAGE_DIR", filepath.Join(root, "missing-store"))
	got, directive := complete(t, "__complete", "email")
	if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("missing store: %q %v", got, directive)
	}
	if _, err := os.Stat(os.Getenv("PASSAGE_DIR")); !os.IsNotExist(err) {
		t.Fatal("completion created the missing store")
	}
	t.Setenv("PASSAGE_DIR", filepath.Join(root, "server.age"))
	got, directive = complete(t, "__complete", "email")
	if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveError {
		t.Fatalf("unreadable store: %q %v", got, directive)
	}
}

func TestCompletionExcludesUnsafeNames(t *testing.T) {
	root := completionFixture(t)
	if runtime.GOOS != "windows" {
		for _, name := range []string{"bad\tname", "bad\nname", "bad\x1bname", "bad:name", "bad?name", "NUL"} {
			if err := os.WriteFile(filepath.Join(root, name+".age"), []byte("not ciphertext"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"bad", "NUL", "escape", ".hidden", ".git"} {
		got, _ := complete(t, "__complete", "show", prefix)
		if len(got) != 0 {
			t.Fatalf("unsafe completion for %q: %q", prefix, got)
		}
	}
}

func TestCompletionListingFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions")
	}
	root := completionFixture(t)
	dir := filepath.Join(root, "email")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	got, directive := complete(t, "__complete", "show", "email")
	if len(got) != 0 || directive != cobra.ShellCompDirectiveError|cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("listing failure: %q, %v", got, directive)
	}
}

func TestCompletionShellNamesAndGeneration(t *testing.T) {
	completionFixture(t)
	got, directive := complete(t, "__complete", "completion", "")
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"bash", "fish", "powershell", "zsh"}) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("shell choices: %q %v", got, directive)
	}
	got, _ = complete(t, "__complete", "completion", "po")
	if !reflect.DeepEqual(got, []string{"powershell"}) {
		t.Fatal(got)
	}
	t.Setenv("PASSAGE_DIR", filepath.Join(t.TempDir(), "missing"))
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		out, err := run(t, "", "completion", shell)
		if err != nil || !strings.Contains(out, "__complete") {
			t.Fatalf("%s script generation: %q, %v", shell, out, err)
		}
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		files[path] = "directory"
		if !entry.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
