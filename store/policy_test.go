package store

import (
	"filippo.io/age"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmptyRecipientPolicyFailsClosed(t *testing.T) {
	s := fixture(t)
	os.WriteFile(filepath.Join(s.fs.config.Dir, ".age-recipients"), nil, 0600)
	if _, e := s.Write("entry", []byte("secret"), WriteOptions{Overwrite: true}); e == nil {
		t.Fatal("empty recipient policy fell back to identities")
	}
}
func TestRecipientPrecedence(t *testing.T) {
	s := fixture(t)
	other, _ := age.GenerateX25519Identity()
	os.Mkdir(filepath.Join(s.fs.config.Dir, "nested"), 0700)
	os.WriteFile(filepath.Join(s.fs.config.Dir, "nested/.age-recipients"), []byte(other.Recipient().String()), 0600)
	if _, e := s.Write("nested/a", []byte("secret"), WriteOptions{Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e == nil {
		t.Fatal("nearest policy ignored")
	}
	root, _ := os.ReadFile(filepath.Join(s.fs.config.Dir, ".age-recipients"))
	s.fs.config.Recipients = strings.TrimSpace(string(root))
	if _, e := s.Write("nested/a", []byte("secret"), WriteOptions{Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e != nil {
		t.Fatal("explicit recipients did not override nested policy", e)
	}
	s.fs.config.RecipientsFile = filepath.Join(s.fs.config.Dir, "nested/.age-recipients")
	if _, e := s.Write("nested/a", []byte("secret"), WriteOptions{Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e == nil {
		t.Fatal("recipient file did not win")
	}
}
