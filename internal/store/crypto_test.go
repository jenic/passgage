package store

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"golang.org/x/crypto/ssh"
)

// The test executable doubles as a real age protocol plugin, without a shell.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "--age-plugin=") && os.Getenv("PASSGAGE_TEST_AGE_PLUGIN") == "1" {
		p, _ := plugin.New("passgagetest")
		p.RegisterFlags(flag.CommandLine)
		p.HandleIdentity(func(b []byte) (age.Identity, error) { return age.ParseX25519Identity(string(b)) })
		p.HandleIdentityAsRecipient(func(b []byte) (age.Recipient, error) {
			id, e := age.ParseX25519Identity(string(b))
			if e != nil {
				return nil, e
			}
			return id.Recipient(), nil
		})
		p.HandleRecipient(func(b []byte) (age.Recipient, error) { return age.ParseX25519Recipient(string(b)) })
		flag.Parse()
		os.Exit(p.Main())
	}
	os.Exit(m.Run())
}
func TestPluginConsentAndRoundTrip(t *testing.T) {
	s := fixture(t)
	id, _ := age.GenerateX25519Identity()
	encoded := plugin.EncodeIdentity("passgagetest", []byte(id.String()))
	os.WriteFile(s.Config.Identities, []byte(encoded+"\n"), 0600)
	os.Remove(filepath.Join(s.Config.Dir, ".age-recipients"))
	if err := s.Write("entry", []byte("secret")); err == nil || !strings.Contains(err.Error(), "requires --allow-age-plugin") {
		t.Fatalf("consent not enforced: %v", err)
	}
	dir := t.TempDir()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	target := filepath.Join(dir, "age-plugin-passgagetest"+suffix)
	src, e := os.Open(exe)
	if e != nil {
		t.Fatal(e)
	}
	dst, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(dst, src)
	src.Close()
	dst.Close()
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PASSGAGE_TEST_AGE_PLUGIN", "1")
	s.Crypto.Config.Plugins = []string{"passgagetest"}
	if err := s.Write("entry", []byte("plugin secret\n")); err != nil {
		t.Fatal(err)
	}
	b, e := s.Read("entry")
	if e != nil || string(b) != "plugin secret\n" {
		t.Fatalf("plugin roundtrip: %q %v", b, e)
	}
	s.Config.Recipients = plugin.EncodeRecipient("passgagetest", []byte(id.Recipient().String()))
	if e = s.Write("explicit", []byte("explicit recipient")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read("explicit"); e != nil {
		t.Fatal(e)
	}
}
func TestEncryptedSSHIdentityRetainsKey(t *testing.T) {
	s := fixture(t)
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	block, e := ssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte("passphrase"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(s.Config.Identities, pem.EncodeToMemory(block), 0600)
	os.Remove(filepath.Join(s.Config.Dir, ".age-recipients"))
	calls := 0
	s.Crypto.Prompt = func(string) (string, error) { calls++; return "passphrase", nil }
	if e = s.Write("ssh", []byte("secret\n")); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		b, e := s.Read("ssh")
		if e != nil || !bytes.Equal(b, []byte("secret\n")) {
			t.Fatalf("SSH: %q %v", b, e)
		}
	}
	if calls != 1 {
		t.Fatalf("prompt count %d", calls)
	}
}
func TestEmptyRecipientPolicyFailsClosed(t *testing.T) {
	s := fixture(t)
	os.WriteFile(filepath.Join(s.Config.Dir, ".age-recipients"), nil, 0600)
	if e := s.Write("entry", []byte("secret")); e == nil {
		t.Fatal("empty recipient policy fell back to identities")
	}
}
func TestRecipientPrecedence(t *testing.T) {
	s := fixture(t)
	other, _ := age.GenerateX25519Identity()
	os.Mkdir(filepath.Join(s.Config.Dir, "nested"), 0700)
	os.WriteFile(filepath.Join(s.Config.Dir, "nested/.age-recipients"), []byte(other.Recipient().String()), 0600)
	if e := s.Write("nested/a", []byte("secret")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e == nil {
		t.Fatal("nearest policy ignored")
	}
	root, _ := os.ReadFile(filepath.Join(s.Config.Dir, ".age-recipients"))
	s.Config.Recipients = strings.TrimSpace(string(root))
	if e := s.Write("nested/a", []byte("secret")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e != nil {
		t.Fatal("explicit recipients did not override nested policy", e)
	}
	s.Config.RecipientsFile = filepath.Join(s.Config.Dir, "nested/.age-recipients")
	if e := s.Write("nested/a", []byte("secret")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read("nested/a"); e == nil {
		t.Fatal("recipient file did not win")
	}
}
