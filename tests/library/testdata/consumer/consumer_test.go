package consumer_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/jenic/passgage/agecrypto"
	"github.com/jenic/passgage/store"
)

func TestStoreWorkflow(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agecrypto.New(agecrypto.Config{Identities: []age.Identity{id}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(store.Config{Dir: filepath.Join(t.TempDir(), "store"), Create: true, Crypto: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Write("team/account", []byte("example\nmetadata\n"), store.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("team/account", nil, store.WriteOptions{}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err := s.Update(func(tx *store.Tx) error {
		if _, err := tx.Copy("team/account", "team/copy", store.CopyOptions{}); err != nil {
			return err
		}
		_, err := tx.Move("team/copy", "team/moved", store.CopyOptions{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	names, err := s.List("team")
	if err != nil || len(names) != 2 {
		t.Fatalf("list: %v %v", names, err)
	}
	if _, err := s.Reencrypt("team"); err != nil {
		t.Fatal(err)
	}
	plain, err := s.Read("team/moved")
	if err != nil || string(plain) != "example\nmetadata\n" {
		t.Fatalf("read: %q %v", plain, err)
	}
	clear(plain)
	if _, err := s.Remove("team", store.RemoveOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
}

func TestFileIdentityInitialization(t *testing.T) {
	dir := t.TempDir()
	opts := store.InitOptions{Dir: filepath.Join(dir, "store"), IdentityFile: filepath.Join(dir, "identities")}
	if err := store.Initialize(opts); err != nil {
		t.Fatal(err)
	}
	engine, err := agecrypto.New(agecrypto.Config{IdentityFile: opts.IdentityFile})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(store.Config{Dir: opts.Dir, Crypto: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	password, err := store.Generate(32, "[:alnum:]")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(password)
	if _, err := s.Write("generated", password, store.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
}
