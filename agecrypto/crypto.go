// Package agecrypto provides native age encryption and passage identity loading.
package agecrypto

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"filippo.io/age/plugin"
	"golang.org/x/crypto/ssh"
)

// ErrPluginNotAllowed reports a plugin absent from the explicit allowlist.
var ErrPluginNotAllowed = errors.New("age plugin is not allowed")

// ErrInteractionRequired reports an operation requiring a caller-provided callback.
var ErrInteractionRequired = errors.New("interactive input required")

// Config explicitly configures an engine. No passage environment settings are read.
type Config struct {
	// IdentityFile names a passage identity file, loaded only when needed.
	IdentityFile string
	// Identities supplies caller-owned age identities instead of an identity file.
	// The slice is copied; its identities must not be mutated during engine use.
	Identities []age.Identity
	// AllowedPlugins lists plugin names permitted to run as child processes.
	AllowedPlugins []string
	// Prompt supplies passphrases for encrypted identity files and SSH keys.
	Prompt func(string) (string, error)
	// PluginUI handles plugin interaction. Missing input callbacks fail closed.
	// WaitTimer may be called concurrently by the age plugin implementation.
	PluginUI *plugin.ClientUI
}

// Engine encrypts and decrypts age messages. Operations are serialized and successful
// identity unlocks are cached for its lifetime. Callbacks must not re-enter the engine.
// An Engine must be constructed with New and must not be copied.
type Engine struct {
	mu         sync.Mutex
	config     Config
	uiMu       sync.Mutex
	uiError    error
	identities []age.Identity
}

// New snapshots configuration without reading identities or starting plugins.
// IdentityFile and Identities are mutually exclusive. Neither is required for
// recipient-only encryption. Relative identity paths are resolved now.
func New(c Config) (*Engine, error) {
	if c.IdentityFile != "" && len(c.Identities) != 0 {
		return nil, errors.New("IdentityFile and Identities are mutually exclusive")
	}
	if c.IdentityFile != "" {
		var err error
		c.IdentityFile, err = filepath.Abs(c.IdentityFile)
		if err != nil {
			return nil, err
		}
	}
	c.Identities = append([]age.Identity(nil), c.Identities...)
	c.AllowedPlugins = append([]string(nil), c.AllowedPlugins...)
	for _, id := range c.Identities {
		if id == nil {
			return nil, errors.New("nil age identity")
		}
	}
	if c.PluginUI != nil {
		ui := *c.PluginUI
		c.PluginUI = &ui
	}
	return &Engine{config: c}, nil
}

func (c *Engine) ask(s string) (string, error) {
	if c.config.Prompt == nil {
		return "", fmt.Errorf("%w: %s", ErrInteractionRequired, s)
	}
	return c.config.Prompt(s)
}
func (c *Engine) ui() *plugin.ClientUI {
	ui := plugin.ClientUI{}
	if c.config.PluginUI != nil {
		ui = *c.config.PluginUI
	}
	// Plugin protocol callbacks communicate failures to the plugin. Keep the
	// original callback error too, so callers can inspect it after the protocol ends.
	request := ui.RequestValue
	ui.RequestValue = func(name, prompt string, secret bool) (string, error) {
		var value string
		var err error
		if request == nil {
			err = fmt.Errorf("%w: plugin %s", ErrInteractionRequired, name)
		} else {
			value, err = request(name, prompt, secret)
		}
		c.recordUIError(err)
		return value, err
	}
	confirm := ui.Confirm
	ui.Confirm = func(name, prompt, yes, no string) (bool, error) {
		var value bool
		var err error
		if confirm == nil {
			err = fmt.Errorf("%w: plugin %s", ErrInteractionRequired, name)
		} else {
			value, err = confirm(name, prompt, yes, no)
		}
		c.recordUIError(err)
		return value, err
	}
	display := ui.DisplayMessage
	ui.DisplayMessage = func(name, msg string) error {
		if display == nil {
			return nil
		}
		err := display(name, msg)
		c.recordUIError(err)
		return err
	}
	return &ui
}
func (c *Engine) allow(name string) error {
	for _, n := range c.config.AllowedPlugins {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrPluginNotAllowed, name)
}
func (c *Engine) load() (ids []age.Identity, result error) {
	defer func() {
		if result != nil {
			c.identities = nil
		}
	}()
	if c.identities != nil {
		return c.identities, nil
	}
	if len(c.config.Identities) != 0 {
		for _, id := range c.config.Identities {
			if p, ok := id.(*plugin.Identity); ok {
				if err := c.allow(p.Name()); err != nil {
					return nil, err
				}
			}
		}
		c.identities = append([]age.Identity(nil), c.config.Identities...)
		return c.identities, nil
	}
	if c.config.IdentityFile == "" {
		return nil, errors.New("no identities configured")
	}
	b, err := os.ReadFile(c.config.IdentityFile)
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
func (c *Engine) Decrypt(b []byte) (result []byte, resultErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetUIError()
	defer func() {
		if err := c.takeUIError(); err != nil {
			clear(result)
			result = nil
			resultErr = errors.Join(resultErr, err)
		}
	}()

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
func (c *Engine) Encrypt(plain []byte, lines string) (result []byte, resultErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetUIError()
	defer func() {
		if err := c.takeUIError(); err != nil {
			clear(result)
			result = nil
			resultErr = errors.Join(resultErr, err)
		}
	}()

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

func (c *Engine) recordUIError(err error) {
	if err == nil {
		return
	}
	c.uiMu.Lock()
	defer c.uiMu.Unlock()
	c.uiError = errors.Join(c.uiError, err)
}
func (c *Engine) resetUIError()      { c.uiMu.Lock(); c.uiError = nil; c.uiMu.Unlock() }
func (c *Engine) takeUIError() error { c.uiMu.Lock(); defer c.uiMu.Unlock(); return c.uiError }
