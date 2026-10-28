package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/jenic/passgage/agecrypto"
)

func TestExplicitConfigurationAndRecipientOnlyWrites(t *testing.T) {
	t.Setenv("PASSAGE_DIR", filepath.Join(t.TempDir(), "ignored"))
	t.Setenv("PASSAGE_AGE", "ignored")
	t.Setenv("PASSAGE_IDENTITIES_FILE", "ignored")
	t.Setenv("PASSAGE_RECIPIENTS", "invalid")
	if _, err := Open(Config{}); err == nil {
		t.Fatal("accepted missing directory")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Dir: filepath.Join(t.TempDir(), "store"), Create: true, Recipients: id.Recipient().String()}
	writer, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	c.Recipients = "invalid" // Open snapshots caller configuration.
	changes, err := writer.Write("service/account", []byte("secret\nmetadata\x00"), WriteOptions{})
	if err != nil || !reflect.DeepEqual(changes.Paths, []string{"service/account.age"}) {
		t.Fatalf("write: %+v, %v", changes, err)
	}
	engine, err := agecrypto.New(agecrypto.Config{Identities: []age.Identity{id}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := Open(Config{Dir: c.Dir, Crypto: engine})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	plain, err := reader.Read("service/account")
	if err != nil || !bytes.Equal(plain, []byte("secret\nmetadata\x00")) {
		t.Fatalf("read: %q, %v", plain, err)
	}
	clear(plain)
}

func TestOverwriteAndErrorInspection(t *testing.T) {
	s := fixture(t)
	if _, err := s.Write("entry", []byte("original"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []func() error{
		func() error { _, e := s.Write("entry", []byte("replacement"), WriteOptions{}); return e },
		func() error {
			_, e := s.Copy("entry", "entry2", CopyOptions{})
			if e != nil {
				return e
			}
			_, e = s.Copy("entry", "entry2", CopyOptions{})
			return e
		},
		func() error { _, e := s.Move("entry", "entry2", CopyOptions{}); return e },
	} {
		if err := op(); !errors.Is(err, os.ErrExist) {
			t.Fatalf("expected exists: %v", err)
		}
	}
	if _, err := s.Write("entry", []byte("replacement"), WriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read("entry"); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement: %q, %v", got, err)
	}
	if ok, err := s.Exists("absent"); err != nil || ok {
		t.Fatalf("absent: %v, %v", ok, err)
	}
	if _, err := s.Exists("../unsafe"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid name: %v", err)
	}

	for _, recursive := range []bool{false, true} {
		if _, err := s.Remove("absent", RemoveOptions{Recursive: recursive}); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing removal: %v", err)
		}
	}
	if _, err := s.Read("absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing entry: %v", err)
	} else {
		var op *OpError
		if !errors.As(err, &op) || op.Op != "read" || op.Name != "absent" {
			t.Fatalf("missing operation context: %v", err)
		}
	}
}

func TestUpdatePartialChangesAndLifetime(t *testing.T) {
	s := fixture(t)
	stop := errors.New("stop after saving")
	var expired *Tx
	changes, err := s.Update(func(tx *Tx) error {
		expired = tx
		for _, name := range []string{"z", "a", "z"} {
			if _, e := tx.Write(name, []byte(name), WriteOptions{Overwrite: true}); e != nil {
				return e
			}
		}
		if got := tx.Changes(); !reflect.DeepEqual(got.Paths, []string{"a.age", "z.age"}) {
			t.Fatalf("snapshot: %+v", got)
		}
		snapshot := tx.Changes()
		snapshot.Paths[0] = "corrupted"
		if got, e := tx.Read("a"); e != nil || string(got) != "a" {
			t.Fatalf("transaction read: %q, %v", got, e)
		}
		return stop
	})
	if !errors.Is(err, stop) || !reflect.DeepEqual(changes.Paths, []string{"a.age", "z.age"}) {
		t.Fatalf("partial result: %+v, %v", changes, err)
	}
	if _, err := expired.Read("a"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expired read: %v", err)
	}
	if _, err := expired.Write("late", []byte("late"), WriteOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expired write: %v", err)
	}
	if got, e := s.Read("z"); e != nil || string(got) != "z" {
		t.Fatalf("completed write rolled back: %q, %v", got, e)
	}
	if _, err := s.Write("after", []byte("after"), WriteOptions{}); err != nil {
		t.Fatal("lock not released", err)
	}
}

func TestUpdatePanicReleasesLock(t *testing.T) {
	s := fixture(t)
	var expired *Tx
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_, _ = s.Update(func(tx *Tx) error { expired = tx; panic("callback") })
	}()
	if _, err := expired.List(""); !errors.Is(err, ErrClosed) {
		t.Fatalf("transaction survived panic: %v", err)
	}
	if _, err := s.Write("after", []byte("safe"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestReencryptReturnsCompletedPaths(t *testing.T) {
	s := fixture(t)
	for _, name := range []string{"a", "z"} {
		if _, err := s.Write(name, []byte("secret"), WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.fs.config.Dir, "z.age"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.fs.config.Dir, "a.age"))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := s.Reencrypt("")
	if err == nil || !reflect.DeepEqual(changes.Paths, []string{"a.age"}) {
		t.Fatalf("partial reencryption: %+v, %v", changes, err)
	}
	after, err := os.ReadFile(filepath.Join(s.fs.config.Dir, "a.age"))
	if err != nil || bytes.Equal(before, after) {
		t.Fatalf("first entry not reencrypted: %v", err)
	}
}

func TestConcurrentStoreUseAndClose(t *testing.T) {
	s := fixture(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			name := fmt.Sprintf("entry%d", i)
			if _, err := s.Write(name, []byte(name), WriteOptions{}); err != nil {
				t.Error(err)
				return
			}
			plain, err := s.Read(name)
			if err != nil || string(plain) != name {
				t.Errorf("read: %q, %v", plain, err)
			}
			clear(plain)
		})
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IsDir(""); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed root: %v", err)
	}
	if _, err := s.Write("closed", nil, WriteOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed mutation: %v", err)
	}
}

func TestInitializeNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	c := InitOptions{Dir: filepath.Join(dir, "store"), IdentityFile: filepath.Join(dir, "identities")}
	if err := Initialize(c); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(c); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate init: %v", err)
	}
	after, err := os.ReadFile(c.IdentityFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("identity changed: %v", err)
	}
}
