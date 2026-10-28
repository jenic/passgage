package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/jenic/passgage/internal/storefs"
)

// InitOptions specifies where to create a new store and identity. Both paths are required.
// Passphrase optionally encrypts the new identity; an empty passphrase leaves it unencrypted.
type InitOptions struct{ Dir, IdentityFile, Passphrase string }

// Initialize creates an identity and root recipients file without overwriting keys.
func Initialize(c InitOptions) (resultErr error) {
	defer func() { resultErr = opError("initialize", c.Dir, resultErr) }()
	if c.Dir == "" || c.IdentityFile == "" {
		return errors.New("Dir and IdentityFile are required")
	}
	var err error
	c.Dir, err = filepath.Abs(c.Dir)
	if err != nil {
		return err
	}
	c.IdentityFile, err = filepath.Abs(c.IdentityFile)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(c.IdentityFile); err == nil {
		return fmt.Errorf("identity already exists; initialization never replaces keys: %w", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(c.Dir, ".age-recipients")); err == nil {
		return fmt.Errorf("recipients already exist: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	b := []byte(id.String() + "\n")
	defer clear(b)
	if c.Passphrase != "" {
		r, err := age.NewScryptRecipient(c.Passphrase)
		if err != nil {
			return err
		}
		var out bytes.Buffer
		a := armor.NewWriter(&out)
		w, err := age.Encrypt(a, r)
		if err != nil {
			return err
		}
		if _, err = w.Write(b); err != nil {
			return err
		}
		if err = w.Close(); err != nil {
			return err
		}
		if err = a.Close(); err != nil {
			return err
		}
		b = out.Bytes()
	}
	if err = storefs.WriteExclusive(c.IdentityFile, b); err != nil {
		return fmt.Errorf("create identity: %w", err)
	}
	if err = storefs.WriteExclusive(c.Dir+"/.age-recipients", []byte(id.Recipient().String()+"\n")); err != nil {
		return fmt.Errorf("identity saved, recipients creation failed: %w", err)
	}
	return nil
}
