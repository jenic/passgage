//go:build reference

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jenic/passgage/agecrypto"
	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/store"
)

func TestOriginalPassageInteroperability(t *testing.T) {
	script := os.Getenv("PASSGAGE_REFERENCE_SCRIPT")
	ageBinary := os.Getenv("PASSGAGE_REFERENCE_AGE")
	if script == "" || ageBinary == "" {
		t.Fatal("set PASSGAGE_REFERENCE_SCRIPT and PASSGAGE_REFERENCE_AGE")
	}
	dir := t.TempDir()
	c := cfg.Config{Dir: filepath.Join(dir, "store"), Identities: filepath.Join(dir, "identities")}
	if e := store.Initialize(store.InitOptions{Dir: c.Dir, IdentityFile: c.Identities}); e != nil {
		t.Fatal(e)
	}
	engine, e := agecrypto.New(agecrypto.Config{IdentityFile: c.Identities})
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(store.Config{Dir: c.Dir, Crypto: engine})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	want := []byte("password fixture\nUnicode: λ\n\n")
	if _, e = s.Write("native/entry", want, store.WriteOptions{}); e != nil {
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
