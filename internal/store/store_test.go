package store

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/jenic/passgage/internal/cfg"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	c := cfg.Config{Dir: filepath.Join(dir, "store"), Identities: filepath.Join(dir, "identities")}
	if err := Initialize(c, ""); err != nil {
		t.Fatal(err)
	}
	s, err := Open(c, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestRoundTripAndAtomicFailure(t *testing.T) {
	s := fixture(t)
	want := []byte("password\nname: λ\n\x00\n\n")
	if err := s.Write("mail/work", want); err != nil {
		t.Fatal(err)
	}
	b, err := s.Read("mail/work")
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("roundtrip %q: %v", b, err)
	}
	cipher, _ := os.ReadFile(filepath.Join(s.Config.Dir, "mail/work.age"))
	if bytes.Contains(cipher, []byte("password")) {
		t.Fatal("plaintext persisted")
	}
	s.Config.Recipients = "invalid"
	if err = s.Write("mail/work", []byte("replacement")); err == nil {
		t.Fatal("invalid recipients accepted")
	}
	after, _ := os.ReadFile(filepath.Join(s.Config.Dir, "mail/work.age"))
	if !bytes.Equal(cipher, after) {
		t.Fatal("failed encryption changed original")
	}
}
func TestCorruptionReturnsNoPlaintext(t *testing.T) {
	s := fixture(t)
	s.Write("entry", bytes.Repeat([]byte("secret"), 20000))
	p := filepath.Join(s.Config.Dir, "entry.age")
	b, _ := os.ReadFile(p)
	b = b[:len(b)-1]
	os.WriteFile(p, b, 0600)
	plain, err := s.Read("entry")
	if err == nil || len(plain) > 0 {
		t.Fatal("returned unauthenticated plaintext")
	}
}
func TestPathValidation(t *testing.T) {
	for _, n := range []string{"", "../x", "x/../y", "/tmp/a", "C:\\x", "a\\b", ".git/config", "foo/.age-recipients", "NUL", "x/COM1.txt", "a.", "x//y"} {
		if _, err := Name(n); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
}
func TestSymlinkEscape(t *testing.T) {
	s := fixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.Config.Dir, "escape")); err != nil {
		t.Skip(err)
	}
	if err := s.Write("escape/entry", []byte("secret")); err == nil {
		t.Fatal("accepted escape")
	}
	if _, err := os.Stat(filepath.Join(outside, "entry.age")); !os.IsNotExist(err) {
		t.Fatal("wrote outside store")
	}
}
func TestDestinationRecipientsAndMoveFailure(t *testing.T) {
	s := fixture(t)
	if err := s.Write("source", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	other, _ := age.GenerateX25519Identity()
	os.Mkdir(filepath.Join(s.Config.Dir, "team"), 0700)
	os.WriteFile(filepath.Join(s.Config.Dir, "team/.age-recipients"), []byte(other.Recipient().String()), 0600)
	if _, err := s.Copy("source", "team/destination", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("team/destination"); err == nil {
		t.Fatal("destination not reencrypted")
	}
	if !s.Exists("source") {
		t.Fatal("copy removed source")
	}
	os.WriteFile(filepath.Join(s.Config.Dir, "team/.age-recipients"), []byte("invalid"), 0600)
	if _, err := s.Copy("source", "team/moved", true, false); err == nil {
		t.Fatal("invalid recipients accepted")
	}
	if !s.Exists("source") {
		t.Fatal("failed move removed source")
	}
}
func TestEncryptedIdentity(t *testing.T) {
	dir := t.TempDir()
	c := cfg.Config{Dir: filepath.Join(dir, "store"), Identities: filepath.Join(dir, "identities")}
	if err := Initialize(c, "testing passphrase"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s, err := Open(c, false, func(string) (string, error) { calls++; return "testing passphrase", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Write("a", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.Read("a"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("prompted %d times", calls)
	}
}
func TestGenerator(t *testing.T) {
	for _, pattern := range []string{"[:alnum:]", "[:punct:][:alnum:]", "a-c", "x"} {
		b, err := Generate(100, pattern)
		if err != nil || len(b) != 100 {
			t.Fatalf("generate: %v", err)
		}
		if pattern == "a-c" && strings.Trim(string(b), "abc") != "" {
			t.Fatal("range not respected")
		}
	}
	for _, p := range []string{"", "[:invalid:]", "z-a"} {
		if _, err := Generate(20, p); err == nil {
			t.Fatal("invalid pattern accepted")
		}
	}
}
func TestStoreLock(t *testing.T) {
	s := fixture(t)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lock(); err == nil {
		t.Fatal("concurrent writer accepted")
	}
	unlock()
	unlock, err = s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestDirectoryMovePreservesPolicies(t *testing.T) {
	s := fixture(t)
	if e := s.Write("source/nested/entry", []byte("secret")); e != nil {
		t.Fatal(e)
	}
	root, e := os.ReadFile(filepath.Join(s.Config.Dir, ".age-recipients"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(s.Config.Dir, "source/nested/.age-recipients"), root, 0600)
	if _, e = s.Copy("source", "destination", true, false); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.Config.Dir, "source")); !os.IsNotExist(e) {
		t.Fatal("source directory remains")
	}
	if _, e = os.Stat(filepath.Join(s.Config.Dir, "destination/nested/.age-recipients")); e != nil {
		t.Fatal("recipient policy not copied", e)
	}
	if _, e = s.Read("destination/nested/entry"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Remove("destination", true); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.Config.Dir, "destination")); !os.IsNotExist(e) {
		t.Fatal("deleted directory remains")
	}
}

func TestWriteFailurePreservesOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission fault injection")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses Unix permission faults")
	}
	s := fixture(t)
	if e := s.Write("entry", []byte("original")); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(s.Config.Dir, "entry.age")
	before, _ := os.ReadFile(p)
	if e := os.Chmod(s.Config.Dir, 0500); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(s.Config.Dir, 0700)
	if e := s.Write("entry", []byte("replacement")); e == nil {
		t.Fatal("expected directory write failure")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed ciphertext")
	}
}
