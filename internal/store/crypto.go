package store

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"filippo.io/age/plugin"
	"github.com/jenic/passgage/internal/cfg"
	"golang.org/x/crypto/ssh"
)

// Crypto implements native age encryption with per-command identity caching.
type Crypto struct {
	Config     cfg.Config
	Prompt     func(string) (string, error)
	identities []age.Identity
}

func (c *Crypto) ask(s string) (string, error) {
	if c.Prompt == nil {
		return "", fmt.Errorf("interactive input required: %s", s)
	}
	return c.Prompt(s)
}
func (c *Crypto) ui() *plugin.ClientUI {
	return &plugin.ClientUI{
		DisplayMessage: func(name, msg string) error { fmt.Fprintf(os.Stderr, "%s: %s\n", name, msg); return nil },
		RequestValue:   func(name, prompt string, secret bool) (string, error) { return c.ask(name + ": " + prompt) },
		Confirm: func(name, prompt, yes, no string) (bool, error) {
			v, err := c.ask(name + ": " + prompt + " [" + yes + "/" + no + "]")
			return v == yes, err
		},
		WaitTimer: func(name string) { fmt.Fprintf(os.Stderr, "Waiting for %s; check your hardware token.\n", name) },
	}
}
func (c *Crypto) allow(name string) error {
	for _, n := range c.Config.Plugins {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("age plugin %q requires --allow-age-plugin=%s or PASSGAGE_AGE_PLUGINS", name, name)
}
func (c *Crypto) load() (ids []age.Identity, result error) {
	defer func() {
		if result != nil {
			c.identities = nil
		}
	}()
	if c.identities != nil {
		return c.identities, nil
	}
	if os.Getenv("PASSAGE_AGE") != "" {
		return nil, fmt.Errorf("PASSAGE_AGE is unsupported: passgage uses native age; unset it")
	}
	b, err := os.ReadFile(c.Config.Identities)
	if err != nil {
		return nil, fmt.Errorf("read identities: %w", err)
	}
	defer clear(b)
	if bytes.HasPrefix(b, []byte("age-encryption.org/")) || bytes.HasPrefix(b, []byte(armor.Header)) {
		pass, e := c.ask("Identity file passphrase: ")
		if e != nil {
			return nil, e
		}
		id, e := age.NewScryptIdentity(pass)
		if e != nil {
			return nil, e
		}
		r, e := age.Decrypt(ageReader(b), id)
		if e != nil {
			return nil, fmt.Errorf("unlock identities: %w", e)
		}
		b, e = io.ReadAll(io.LimitReader(r, (4<<20)+1))
		if e != nil {
			return nil, e
		}
		defer clear(b)
		if len(b) > 4<<20 {
			return nil, fmt.Errorf("identity file exceeds 4 MiB")
		}
	}
	if bytes.HasPrefix(b, []byte("-----BEGIN ")) {
		id, e := agessh.ParseIdentity(b)
		var missing *ssh.PassphraseMissingError
		if errors.As(e, &missing) && missing.PublicKey != nil {
			id, e = agessh.NewEncryptedSSHIdentity(missing.PublicKey, bytes.Clone(b), func() ([]byte, error) { p, e := c.ask("SSH identity passphrase: "); return []byte(p), e })
		} else if errors.As(e, &missing) {
			pass, err := c.ask("SSH identity passphrase: ")
			if err != nil {
				return nil, err
			}
			key, err := ssh.ParseRawPrivateKeyWithPassphrase(b, []byte(pass))
			if err != nil {
				return nil, err
			}
			switch key := key.(type) {
			case *rsa.PrivateKey:
				id, e = agessh.NewRSAIdentity(key)
			case *ed25519.PrivateKey:
				id, e = agessh.NewEd25519Identity(*key)
			default:
				return nil, fmt.Errorf("unsupported encrypted SSH key type")
			}
		}
		if e != nil {
			return nil, fmt.Errorf("invalid SSH identity: %w", e)
		}
		c.identities = []age.Identity{id}
		return c.identities, nil
	}
	scan := bufio.NewScanner(bytes.NewReader(b))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "AGE-PLUGIN-") {
			id, e := plugin.NewIdentity(line, c.ui())
			if e != nil {
				return nil, fmt.Errorf("invalid plugin identity")
			}
			if e = c.allow(id.Name()); e != nil {
				return nil, e
			}
			c.identities = append(c.identities, id)
		} else {
			ids, e := age.ParseIdentities(strings.NewReader(line))
			if e != nil {
				return nil, fmt.Errorf("invalid age identity")
			}
			c.identities = append(c.identities, ids...)
		}
	}
	if err = scan.Err(); err != nil {
		return nil, err
	}
	if len(c.identities) == 0 {
		return nil, fmt.Errorf("identity file contains no identities")
	}
	return c.identities, nil
}
func ageReader(b []byte) io.Reader {
	if bytes.HasPrefix(b, []byte(armor.Header)) {
		return armor.NewReader(bytes.NewReader(b))
	}
	return bytes.NewReader(b)
}

// Decrypt authenticates the entire age message before returning plaintext.
func (c *Crypto) Decrypt(b []byte) ([]byte, error) {
	ids, err := c.load()
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(ageReader(b), ids...)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		clear(plain)
		return nil, err
	}
	return plain, nil
}

// Encrypt encrypts plaintext to recipient lines or, when absent, local identities.
func (c *Crypto) Encrypt(plain []byte, lines string) ([]byte, error) {
	if os.Getenv("PASSAGE_AGE") != "" {
		return nil, fmt.Errorf("PASSAGE_AGE is unsupported; unset it")
	}
	var recipients []age.Recipient
	if lines == "" {
		ids, err := c.load()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			switch id := id.(type) {
			case *age.X25519Identity:
				recipients = append(recipients, id.Recipient())
			case *agessh.RSAIdentity:
				recipients = append(recipients, id.Recipient())
			case *agessh.Ed25519Identity:
				recipients = append(recipients, id.Recipient())
			case *agessh.EncryptedSSHIdentity:
				recipients = append(recipients, id.Recipient())
			case *plugin.Identity:
				recipients = append(recipients, id.Recipient())
			default:
				return nil, fmt.Errorf("identity requires an explicit recipients file")
			}
		}
	} else {
		scan := bufio.NewScanner(strings.NewReader(lines))
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var r age.Recipient
			var err error
			if strings.HasPrefix(line, "ssh-") {
				r, err = agessh.ParseRecipient(line)
			} else if strings.HasPrefix(line, "age1") {
				var rs []age.Recipient
				rs, err = age.ParseRecipients(strings.NewReader(line))
				if err == nil && len(rs) == 1 {
					r = rs[0]
				} else {
					var pr *plugin.Recipient
					pr, err = plugin.NewRecipient(line, c.ui())
					if err == nil {
						err = c.allow(pr.Name())
						r = pr
					}
				}
			} else {
				err = fmt.Errorf("unrecognized recipient")
			}
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, fmt.Errorf("invalid recipient")
			}
			recipients = append(recipients, r)
		}
		if err := scan.Err(); err != nil {
			return nil, err
		}
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no recipients configured")
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, recipients...)
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(plain); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Initialize creates an identity and root recipients file without overwriting keys.
func Initialize(c cfg.Config, passphrase string) error {
	if _, err := os.Stat(c.Identities); err == nil {
		return fmt.Errorf("identity already exists; initialization never replaces keys")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(c.Dir + "/.age-recipients"); err == nil {
		return fmt.Errorf("recipients already exist")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	b := []byte(id.String() + "\n")
	defer clear(b)
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
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
	if err = writeExclusive(c.Identities, b); err != nil {
		return fmt.Errorf("create identity: %w", err)
	}
	if err = writeExclusive(c.Dir+"/.age-recipients", []byte(id.Recipient().String()+"\n")); err != nil {
		return fmt.Errorf("identity saved, recipients creation failed: %w", err)
	}
	return nil
}
