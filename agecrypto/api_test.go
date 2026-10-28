package agecrypto

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"filippo.io/age/plugin"
)

func TestInMemoryIdentitySnapshotAndConcurrentUse(t *testing.T) {
	t.Setenv("PASSAGE_AGE", "ignored")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ids := []age.Identity{id}
	engine, err := New(Config{Identities: ids})
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = nil
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			cipher, err := engine.Encrypt([]byte("secret\x00\n"), "")
			if err != nil {
				t.Error(err)
				return
			}
			plain, err := engine.Decrypt(cipher)
			if err != nil || !bytes.Equal(plain, []byte("secret\x00\n")) {
				t.Errorf("roundtrip: %q, %v", plain, err)
			}
			clear(plain)
		})
	}
	wg.Wait()
	if _, err := New(Config{IdentityFile: "file", Identities: []age.Identity{id}}); err == nil {
		t.Fatal("accepted ambiguous identity sources")
	}
	if _, err := New(Config{Identities: []age.Identity{nil}}); err == nil {
		t.Fatal("accepted nil identity")
	}
}

func TestLazyIdentityFileAndAuthenticatedArmor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities")
	reader, err := New(Config{IdentityFile: path})
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := writer.Encrypt([]byte("secret"), id.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := reader.Decrypt(cipher); !errors.Is(err, os.ErrNotExist) || len(plain) != 0 {
		t.Fatalf("missing identity: %q, %v", plain, err)
	}
	if err := os.WriteFile(path, []byte(id.String()), 0600); err != nil {
		t.Fatal(err)
	}
	var armored bytes.Buffer
	w := armor.NewWriter(&armored)
	if _, err := w.Write(cipher); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if plain, err := reader.Decrypt(armored.Bytes()); err != nil || string(plain) != "secret" {
		t.Fatalf("armor: %q, %v", plain, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if plain, err := reader.Decrypt(cipher); err != nil || string(plain) != "secret" {
		t.Fatalf("cached identities: %q, %v", plain, err)
	}
	if plain, err := reader.Decrypt(cipher[:len(cipher)-1]); err == nil || len(plain) != 0 {
		t.Fatalf("unauthenticated output: %q, %v", plain, err)
	}
	if _, err := writer.Encrypt([]byte("secret"), "\n# empty policy\n"); err == nil {
		t.Fatal("empty policy accepted")
	}
}

func TestPromptAndPluginCallbacks(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := age.NewScryptRecipient("passphrase")
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var encrypted bytes.Buffer
	w, err := age.Encrypt(&encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(id.String())); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identities")
	if err := os.WriteFile(path, encrypted.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := New(Config{IdentityFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Encrypt([]byte("secret"), ""); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("missing prompt: %v", err)
	}
	rejected := errors.New("caller refused")
	engine, err = New(Config{IdentityFile: path, Prompt: func(string) (string, error) { return "", rejected }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Encrypt([]byte("secret"), ""); !errors.Is(err, rejected) {
		t.Fatalf("prompt error: %v", err)
	}
	// Check protocol adapters preserve callback errors, including missing input.
	ui := engine.ui()
	if _, err := ui.RequestValue("test", "PIN", true); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("missing plugin input: %v", err)
	}
	engine, err = New(Config{PluginUI: &plugin.ClientUI{DisplayMessage: func(string, string) error { return rejected }, Confirm: func(string, string, string, string) (bool, error) { return false, rejected }}})
	if err != nil {
		t.Fatal(err)
	}
	ui = engine.ui()
	if err := ui.DisplayMessage("test", "message"); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if _, err := ui.Confirm("test", "confirm", "yes", "no"); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if !errors.Is(engine.takeUIError(), rejected) {
		t.Fatal("lost callback error")
	}
}

func TestInMemoryPluginRequiresConsent(t *testing.T) {
	encoded := plugin.EncodeIdentity("unapproved", []byte("identity"))
	id, err := plugin.NewIdentity(encoded, nil)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"different"}
	engine, err := New(Config{Identities: []age.Identity{id}, AllowedPlugins: allowed})
	if err != nil {
		t.Fatal(err)
	}
	allowed[0] = "unapproved"
	if _, err := engine.Encrypt([]byte("secret"), ""); !errors.Is(err, ErrPluginNotAllowed) {
		t.Fatalf("plugin consent: %v", err)
	}
}
