package store_test

import (
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/jenic/passgage/agecrypto"
	"github.com/jenic/passgage/store"
)

func ExampleInitialize() {
	dir, err := os.MkdirTemp("", "passgage-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	opts := store.InitOptions{Dir: filepath.Join(dir, "store"), IdentityFile: filepath.Join(dir, "identities")}
	if err := store.Initialize(opts); err != nil {
		panic(err)
	}
	// Existing passage stores need only the engine and Open calls below.
	engine, err := agecrypto.New(agecrypto.Config{IdentityFile: opts.IdentityFile})
	if err != nil {
		panic(err)
	}
	s, err := store.Open(store.Config{Dir: opts.Dir, Crypto: engine})
	if err != nil {
		panic(err)
	}
	defer s.Close()
	changes, err := s.Write("mail/work", []byte("example\n"), store.WriteOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(changes.Paths)
	// Output: [mail/work.age]
}

func ExampleStore_Update() {
	dir, err := os.MkdirTemp("", "passgage-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	engine, err := agecrypto.New(agecrypto.Config{Identities: []age.Identity{id}})
	if err != nil {
		panic(err)
	}
	s, err := store.Open(store.Config{Dir: dir, Crypto: engine})
	if err != nil {
		panic(err)
	}
	defer s.Close()
	changes, err := s.Update(func(tx *store.Tx) error {
		_, err := tx.Write("account", []byte("example"), store.WriteOptions{Overwrite: true})
		if err != nil {
			return err
		}
		plain, err := tx.Read("account")
		if err != nil {
			return err
		}
		defer clear(plain)
		_, err = tx.Write("backup", plain, store.WriteOptions{})
		return err
	})
	// Always examine changes even when err != nil: Update does not roll back.
	if err != nil {
		panic(err)
	}
	fmt.Println(changes.Paths)
	// Output: [account.age backup.age]
}
