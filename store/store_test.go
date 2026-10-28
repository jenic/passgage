package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/jenic/passgage/agecrypto"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	c := InitOptions{Dir: filepath.Join(dir, "store"), IdentityFile: filepath.Join(dir, "identities")}
	if err := Initialize(c); err != nil {
		t.Fatal(err)
	}
	s, err := openFixture(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestRoundTripAndAtomicFailure(t *testing.T) {
	s := fixture(t)
	want := []byte("password\nname: λ\n\x00\n\n")
	if _, err := s.Write("mail/work", want, WriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	b, err := s.Read("mail/work")
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("roundtrip %q: %v", b, err)
	}
	cipher, _ := os.ReadFile(filepath.Join(s.fs.config.Dir, "mail/work.age"))
	if bytes.Contains(cipher, []byte("password")) {
		t.Fatal("plaintext persisted")
	}
	s.fs.config.Recipients = "invalid"
	if _, err = s.Write("mail/work", []byte("replacement"), WriteOptions{Overwrite: true}); err == nil {
		t.Fatal("invalid recipients accepted")
	}
	after, _ := os.ReadFile(filepath.Join(s.fs.config.Dir, "mail/work.age"))
	if !bytes.Equal(cipher, after) {
		t.Fatal("failed encryption changed original")
	}
}
func TestCorruptionReturnsNoPlaintext(t *testing.T) {
	s := fixture(t)
	_, _ = s.Write("entry", bytes.Repeat([]byte("secret"), 20000), WriteOptions{Overwrite: true})
	p := filepath.Join(s.fs.config.Dir, "entry.age")
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
	if err := os.Symlink(outside, filepath.Join(s.fs.config.Dir, "escape")); err != nil {
		t.Skip(err)
	}
	if _, err := s.Write("escape/entry", []byte("secret"), WriteOptions{Overwrite: true}); err == nil {
		t.Fatal("accepted escape")
	}
	if _, err := os.Stat(filepath.Join(outside, "entry.age")); !os.IsNotExist(err) {
		t.Fatal("wrote outside store")
	}
}
func TestDestinationRecipientsAndMoveFailure(t *testing.T) {
	s := fixture(t)
	if _, err := s.Write("source", []byte("secret"), WriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	other, _ := age.GenerateX25519Identity()
	os.Mkdir(filepath.Join(s.fs.config.Dir, "team"), 0700)
	os.WriteFile(filepath.Join(s.fs.config.Dir, "team/.age-recipients"), []byte(other.Recipient().String()), 0600)
	if _, err := s.Copy("source", "team/destination", CopyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("team/destination"); err == nil {
		t.Fatal("destination not reencrypted")
	}
	if exists, err := s.Exists("source"); err != nil || !exists {
		t.Fatal("copy removed source")
	}
	os.WriteFile(filepath.Join(s.fs.config.Dir, "team/.age-recipients"), []byte("invalid"), 0600)
	if _, err := s.Move("source", "team/moved", CopyOptions{}); err == nil {
		t.Fatal("invalid recipients accepted")
	}
	if exists, err := s.Exists("source"); err != nil || !exists {
		t.Fatal("failed move removed source")
	}
}
func TestEncryptedIdentity(t *testing.T) {
	dir := t.TempDir()
	c := InitOptions{Dir: filepath.Join(dir, "store"), IdentityFile: filepath.Join(dir, "identities")}
	if err := Initialize(InitOptions{Dir: c.Dir, IdentityFile: c.IdentityFile, Passphrase: "testing passphrase"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s, err := openFixture(c, func(string) (string, error) { calls++; return "testing passphrase", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Write("a", []byte("secret"), WriteOptions{Overwrite: true}); err != nil {
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
	other, err := Open(s.fs.config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = s.Update(func(tx *Tx) error {
		if _, e := other.Write("contended", []byte("secret"), WriteOptions{}); !errors.Is(e, ErrBusy) {
			t.Fatalf("concurrent writer: %v", e)
		}
		_, e := tx.Write("held", []byte("secret"), WriteOptions{})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Write("after", []byte("secret"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryMovePreservesPolicies(t *testing.T) {
	s := fixture(t)
	if _, e := s.Write("source/nested/entry", []byte("secret"), WriteOptions{Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	root, e := os.ReadFile(filepath.Join(s.fs.config.Dir, ".age-recipients"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(s.fs.config.Dir, "source/nested/.age-recipients"), root, 0600)
	if _, e = s.Move("source", "destination", CopyOptions{}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.fs.config.Dir, "source")); !os.IsNotExist(e) {
		t.Fatal("source directory remains")
	}
	if _, e = os.Stat(filepath.Join(s.fs.config.Dir, "destination/nested/.age-recipients")); e != nil {
		t.Fatal("recipient policy not copied", e)
	}
	if _, e = s.Read("destination/nested/entry"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Remove("destination", RemoveOptions{Recursive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.fs.config.Dir, "destination")); !os.IsNotExist(e) {
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
	if _, e := s.Write("entry", []byte("original"), WriteOptions{Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(s.fs.config.Dir, "entry.age")
	before, _ := os.ReadFile(p)
	if e := os.Chmod(s.fs.config.Dir, 0500); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(s.fs.config.Dir, 0700)
	if _, e := s.Write("entry", []byte("replacement"), WriteOptions{Overwrite: true}); e == nil {
		t.Fatal("expected directory write failure")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed ciphertext")
	}
}

func openFixture(c InitOptions, prompt func(string) (string, error)) (*Store, error) {
	engine, err := agecrypto.New(agecrypto.Config{IdentityFile: c.IdentityFile, Prompt: prompt})
	if err != nil {
		return nil, err
	}
	return Open(Config{Dir: c.Dir, Crypto: engine})
}
