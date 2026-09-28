//go:build reference

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/internal/store"
)

func TestOriginalPassageInteroperability(t *testing.T) {
	script := os.Getenv("PASSGAGE_REFERENCE_SCRIPT")
	ageBinary := os.Getenv("PASSGAGE_REFERENCE_AGE")
	if script == "" || ageBinary == "" {
		t.Fatal("set PASSGAGE_REFERENCE_SCRIPT and PASSGAGE_REFERENCE_AGE")
	}
	dir := t.TempDir()
	c := cfg.Config{Dir: filepath.Join(dir, "store"), Identities: filepath.Join(dir, "identities")}
	if e := store.Initialize(c, ""); e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(c, false, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	want := []byte("password fixture\nUnicode: λ\n\n")
	if e = s.Write("native/entry", want); e != nil {
		t.Fatal(e)
	}
	shell := func(input []byte, args ...string) []byte {
		t.Helper()
		argv := append([]string{script}, args...)
		cmd := exec.Command("bash", argv...)
		cmd.Env = append(os.Environ(), "PASSAGE_DIR="+c.Dir, "PASSAGE_IDENTITIES_FILE="+c.Identities, "PASSAGE_AGE="+ageBinary)
		cmd.Stdin = bytes.NewReader(input)
		cmd.Stderr = os.Stderr
		b, e := cmd.Output()
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	if got := shell(nil, "show", "native/entry"); !bytes.Equal(got, want) {
		t.Fatalf("shell decrypted %q", got)
	}
	shell(want, "insert", "-m", "shell/entry")
	got, e := s.Read("shell/entry")
	if e != nil || !bytes.Equal(got, want) {
		t.Fatalf("native decrypted %q: %v", got, e)
	}
}
