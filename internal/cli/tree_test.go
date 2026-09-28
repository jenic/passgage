package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

func TestRenderTree(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		entries      []string
		want         string
	}{
		{"empty", "", nil, "Passgage\n"},
		{"empty directory", "empty/", nil, "empty\n"},
		{"single", "", []string{"entry"}, "Passgage\n└── entry\n"},
		{"mixed", "", []string{"z", "b/two", "b/deep/one", "A"}, "Passgage\n├── A\n├── b\n│   ├── deep\n│   │   └── one\n│   └── two\n└── z\n"},
		{"subdirectory", "a/b/", []string{"a/b/é space", "a/b/c/日本語"}, "a/b\n├── c\n│   └── 日本語\n└── é space\n"},
		{"collision", "", []string{"foo", "foo/bar"}, "Passgage\n├── foo\n│   └── bar\n└── foo\n"},
		{"controls", "", []string{"a\x1b[31m\n\t"}, "Passgage\n└── a\\x1b[31m\\n\\t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderTree(&out, tc.prefix, tc.entries, false); err != nil || out.String() != tc.want {
				t.Fatalf("got %q, %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}

func TestTreeColor(t *testing.T) {
	var out bytes.Buffer
	if err := renderTree(&out, "", []string{"dir/entry"}, true); err != nil {
		t.Fatal(err)
	}
	if want := "Passgage\n└── \x1b[1;34mdir\x1b[0m\n    └── entry\n"; out.String() != want {
		t.Fatalf("%q", out.String())
	}
	for _, tc := range []struct {
		name            string
		tty, fail, want bool
		env             map[string]string
	}{
		{"terminal", true, false, true, nil},
		{"pipe forced", false, false, false, map[string]string{"CLICOLOR_FORCE": "1"}},
		{"no color", true, false, false, map[string]string{"NO_COLOR": "yes", "CLICOLOR_FORCE": "1"}},
		{"clicolor zero", true, false, false, map[string]string{"CLICOLOR": "0"}},
		{"dumb", true, false, false, map[string]string{"TERM": "dumb"}},
		{"console setup failure", true, true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			output := termenv.NewOutput(io.Discard, termenv.WithTTY(tc.tty), termenv.WithProfile(termenv.ANSI))
			restored := false
			color, restore := listingColor(output, func(k string) string { return tc.env[k] }, func(*termenv.Output) (func() error, error) {
				if tc.fail {
					return nil, errors.New("console failure")
				}
				return func() error { restored = true; return nil }, nil
			})
			if color != tc.want {
				t.Fatalf("color=%v", color)
			}
			if err := restore(); err != nil || restored != tc.want {
				t.Fatalf("restore=%v, %v", restored, err)
			}
		})
	}
	// Exercise the real writer path, not just the injected color policy.
	t.Setenv("CLICOLOR_FORCE", "1")
	out.Reset()
	if err := writeListing(&out, "", []string{"dir/entry"}); err != nil || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("redirected listing: %q %v", out.String(), err)
	}
}

type failingTreeWriter struct{ err error }

func (w failingTreeWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTreeWriteErrors(t *testing.T) {
	sentinel := errors.New("write failed")
	if err := renderTree(failingTreeWriter{sentinel}, "", nil, false); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := renderTree(failingTreeWriter{}, "", nil, false); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}
