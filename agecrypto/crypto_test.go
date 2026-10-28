package agecrypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
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
		p.HandleRecipient(func(b []byte) (age.Recipient, error) {
			if os.Getenv("PASSGAGE_TEST_PLUGIN_INTERACTION") == "request" {
				if err := p.DisplayMessage("test message"); err != nil {
					return nil, err
				}
				value, err := p.RequestValue("PIN", true)
				if err != nil {
					return nil, err
				}
				if value != "pin" {
					return nil, errors.New("incorrect test PIN")
				}
			}
			return age.ParseX25519Recipient(string(b))
		})
		flag.Parse()
		os.Exit(p.Main())
	}
	os.Exit(m.Run())
}
func TestPluginConsentAndRoundTrip(t *testing.T) {
	c := Config{IdentityFile: filepath.Join(t.TempDir(), "identities")}
	s, _ := New(c)
	id, _ := age.GenerateX25519Identity()
	encoded := plugin.EncodeIdentity("passgagetest", []byte(id.String()))
	os.WriteFile(c.IdentityFile, []byte(encoded+"\n"), 0600)

	if _, err := s.Encrypt([]byte("secret"), ""); err == nil || !errors.Is(err, ErrPluginNotAllowed) {
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
	c.AllowedPlugins = []string{"passgagetest"}
	s, _ = New(c)
	cipher, err := s.Encrypt([]byte("plugin secret\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, e := s.Decrypt(cipher)
	if e != nil || string(b) != "plugin secret\n" {
		t.Fatalf("plugin roundtrip: %q %v", b, e)
	}
	recipients := plugin.EncodeRecipient("passgagetest", []byte(id.Recipient().String()))
	cipher, e = s.Encrypt([]byte("explicit recipient"), recipients)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Decrypt(cipher); e != nil {
		t.Fatal(e)
	}
	// Exercise real protocol interaction, including errors hidden by plugin failure stanzas.
	t.Setenv("PASSGAGE_TEST_PLUGIN_INTERACTION", "request")
	s, _ = New(c)
	if _, err := s.Encrypt([]byte("secret"), recipients); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("missing plugin interaction: %v", err)
	}
	rejected := errors.New("PIN canceled")
	c.PluginUI = &plugin.ClientUI{RequestValue: func(string, string, bool) (string, error) { return "", rejected }}
	s, _ = New(c)
	if _, err := s.Encrypt([]byte("secret"), recipients); !errors.Is(err, rejected) {
		t.Fatalf("plugin callback error: %v", err)
	}
	messages, requests := 0, 0
	c.PluginUI = &plugin.ClientUI{
		DisplayMessage: func(string, string) error { messages++; return nil },
		RequestValue: func(name, prompt string, secret bool) (string, error) {
			requests++
			if name != "passgagetest" || prompt != "PIN" || !secret {
				t.Error("unexpected plugin request")
			}
			return "pin", nil
		},
	}
	s, _ = New(c)
	// Mutating the caller's UI after construction must not alter the engine.
	c.PluginUI.RequestValue = nil
	if _, err := s.Encrypt([]byte("secret"), recipients); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || requests != 1 {
		t.Fatalf("plugin callbacks: messages=%d requests=%d", messages, requests)
	}

}
func TestEncryptedSSHIdentityRetainsKey(t *testing.T) {
	c := Config{IdentityFile: filepath.Join(t.TempDir(), "identities")}
	s, _ := New(c)
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	block, e := ssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte("passphrase"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(c.IdentityFile, pem.EncodeToMemory(block), 0600)

	calls := 0
	c.Prompt = func(string) (string, error) { calls++; return "passphrase", nil }
	s, _ = New(c)
	cipher, e := s.Encrypt([]byte("secret\n"), "")
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		b, e := s.Decrypt(cipher)
		if e != nil || !bytes.Equal(b, []byte("secret\n")) {
			t.Fatalf("SSH: %q %v", b, e)
		}
	}
	if calls != 1 {
		t.Fatalf("prompt count %d", calls)
	}
}
