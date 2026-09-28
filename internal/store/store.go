// Package store manages encrypted passage-compatible entries.
package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/jenic/passgage/internal/cfg"
)

// Store owns a rooted filesystem and encryption configuration.
type Store struct {
	Config cfg.Config
	Crypto *Crypto
	root   *os.Root
}

// Open opens an existing store, optionally creating it.
func Open(c cfg.Config, create bool, prompt func(string) (string, error)) (*Store, error) {
	if create {
		if err := os.MkdirAll(c.Dir, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(c.Dir)
	if err != nil {
		return nil, err
	}
	return &Store{Config: c, Crypto: &Crypto{Config: c, Prompt: prompt}, root: root}, nil
}

// Close releases the directory handle.
func (s *Store) Close() error { return s.root.Close() }

// Name validates portable logical entry paths.
func Name(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00:\r\n") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid entry path %q", name)
	}
	name = strings.TrimSuffix(name, "/")
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") || strings.ContainsAny(p, "<>\"|?*") {
			return "", fmt.Errorf("unsafe or nonportable entry path %q", name)
		}
		base := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", fmt.Errorf("reserved Windows name %q", p)
		}
	}
	return name, nil
}
func (s *Store) check(name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := s.root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink entries are unsupported: %s", p)
		}
	}
	return nil
}

// Lock serializes cooperating mutations. The caller must release the lock.
func (s *Store) Lock() (func(), error) {
	p := filepath.Join(s.Config.Dir, ".passgage.lock")
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe store lock")
	}
	l := flock.New(p)
	ok, err := l.TryLock()
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	if !ok {
		_ = l.Close()
		return nil, fmt.Errorf("store is busy; retry after the other command finishes")
	}
	return func() { _ = l.Unlock(); _ = l.Close() }, nil
}

// Read decrypts and fully authenticates an entry before returning any plaintext.
func (s *Store) Read(name string) ([]byte, error) {
	n, err := Name(name)
	if err != nil {
		return nil, err
	}
	if err = s.check(n + ".age"); err != nil {
		return nil, err
	}
	b, err := s.root.ReadFile(n + ".age")
	if err != nil {
		return nil, err
	}
	return s.Crypto.Decrypt(b)
}

// Exists reports whether an encrypted entry exists.
func (s *Store) Exists(name string) bool {
	n, err := Name(name)
	if err != nil {
		return false
	}
	fi, err := s.root.Lstat(n + ".age")
	return err == nil && fi.Mode().IsRegular()
}

// IsDir reports whether a logical directory exists.
func (s *Store) IsDir(name string) bool {
	if name == "" {
		return true
	}
	n, err := Name(name)
	if err != nil {
		return false
	}
	fi, err := s.root.Lstat(n)
	return err == nil && fi.IsDir()
}

// List returns sorted logical entry names, excluding metadata and symlinks.
func (s *Store) List(prefix string) ([]string, error) {
	base := "."
	if prefix != "" {
		var err error
		base, err = Name(prefix)
		if err != nil {
			return nil, err
		}
		if err = s.check(base); err != nil {
			return nil, err
		}
	}
	var names []string
	err := fs.WalkDir(s.root.FS(), base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(p, ".age") {
			names = append(names, strings.TrimSuffix(p, ".age"))
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

// Write encrypts an entry and atomically replaces its ciphertext.
func (s *Store) Write(name string, plain []byte) error {
	n, err := Name(name)
	if err != nil {
		return err
	}
	if err = s.check(n + ".age"); err != nil {
		return err
	}
	rec, err := s.recipients(path.Dir(n))
	if err != nil {
		return err
	}
	b, err := s.Crypto.Encrypt(plain, rec)
	if err != nil {
		return err
	}
	return s.writeCipher(n, b)
}
func (s *Store) writeCipher(n string, b []byte) error {
	return s.writeFile(n+".age", b)
}
func (s *Store) writeFile(n string, b []byte) error {
	if err := s.check(n); err != nil {
		return err
	}
	if err := s.root.MkdirAll(path.Dir(n), 0700); err != nil {
		return err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(n), fmt.Sprintf(".passgage-%x", token))
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(tmp)
	if err = restrictFile(f); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return s.root.Rename(tmp, n)
}

// GitFiles returns entries and recipient policies suitable for an initial commit.
func (s *Store) GitFiles() ([]string, error) {
	var names []string
	err := fs.WalkDir(s.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".age") || d.Name() == ".age-recipients" || p == ".gitignore" {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

// SetupGitIgnore preserves existing rules and excludes internal coordination files.
func (s *Store) SetupGitIgnore() error {
	if err := s.check(".gitignore"); err != nil {
		return err
	}
	b, err := s.root.ReadFile(".gitignore")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(b)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	for _, rule := range []string{".passgage.lock", ".passgage-*"} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			if line == rule {
				found = true
			}
		}
		if !found {
			text += rule + "\n"
		}
	}
	return s.writeFile(".gitignore", []byte(text))
}
func (s *Store) recipients(dir string) (string, error) {
	return s.recipientsWithPolicies(dir, nil)
}
func (s *Store) recipientsWithPolicies(dir string, policies map[string][]byte) (string, error) {
	if s.Config.RecipientsFile != "" {
		b, err := os.ReadFile(s.Config.RecipientsFile)
		return string(b) + "\n", err
	}
	if s.Config.Recipients != "" {
		return strings.Join(strings.Fields(s.Config.Recipients), "\n") + "\n", nil
	}
	for {
		p := path.Join(dir, ".age-recipients")
		if b, ok := policies[p]; ok {
			return string(b) + "\n", nil
		}
		if err := s.check(p); err != nil {
			return "", err
		}
		b, err := s.root.ReadFile(p)
		if err == nil {
			return string(b) + "\n", nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if dir == "." {
			break
		}
		dir = path.Dir(dir)
	}
	return "", nil
}

func (s *Store) policies(prefix string) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := fs.WalkDir(s.root.FS(), prefix, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != prefix && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != ".age-recipients" {
			return nil
		}
		if err := s.check(p); err != nil {
			return err
		}
		b, err := s.root.ReadFile(p)
		if err == nil {
			result[p] = b
		}
		return err
	})
	return result, err
}
func (s *Store) prune(dir string) {
	for dir != "." && dir != "" {
		if err := s.root.Remove(dir); err != nil {
			return
		}
		dir = path.Dir(dir)
	}
}

// Remove deletes entries and their subtree recipient policies, preserving other files.
func (s *Store) Remove(name string, recursive bool) ([]string, error) {
	n, err := Name(name)
	if err != nil {
		return nil, err
	}
	if err = s.check(n); err != nil {
		return nil, err
	}
	if s.Exists(n) {
		err = s.root.Remove(n + ".age")
		if err != nil {
			return nil, err
		}
		s.prune(path.Dir(n))
		return []string{n + ".age"}, err
	}
	if !recursive {
		return nil, fmt.Errorf("directory removal requires --recursive")
	}
	entries, err := s.List(n)
	if err != nil {
		return nil, err
	}
	policies, err := s.policies(n)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 && len(policies) == 0 {
		return nil, fmt.Errorf("no entries under %s", n)
	}
	var changed []string
	for _, entry := range entries {
		if err = s.root.Remove(entry + ".age"); err != nil {
			return changed, err
		}
		changed = append(changed, entry+".age")
		s.prune(path.Dir(entry))
	}
	for p := range policies {
		if err = s.root.Remove(p); err != nil {
			return changed, err
		}
		changed = append(changed, p)
		s.prune(path.Dir(p))
	}
	s.prune(n)
	return changed, nil
}

// Copy copies or moves entries, encrypting all destinations before touching sources.
func (s *Store) Copy(src, dst string, move, force bool) ([]string, error) {
	src, err := Name(src)
	if err != nil {
		return nil, err
	}
	trailing := strings.HasSuffix(dst, "/")
	dst, err = Name(dst)
	if err != nil {
		return nil, err
	}
	var entries []string
	directory := !s.Exists(src)
	if directory {
		entries, err = s.List(src)
		if err != nil {
			return nil, err
		}
	} else {
		entries = []string{src}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries to copy")
	}
	if s.IsDir(dst) || trailing {
		dst = path.Join(dst, path.Base(src))
	}
	if src == dst || strings.HasPrefix(dst, src+"/") {
		return nil, fmt.Errorf("destination overlaps source")
	}
	policies := map[string][]byte{}
	originalPolicies := map[string][]byte{}
	if directory {
		originalPolicies, err = s.policies(src)
		if err != nil {
			return nil, err
		}
		for p, b := range originalPolicies {
			target := path.Join(dst, strings.TrimPrefix(p, src+"/"))
			if err = s.check(target); err != nil {
				return nil, err
			}
			existing, e := s.root.ReadFile(target)
			if e == nil && !force && string(existing) != string(b) {
				return nil, fmt.Errorf("recipient policy %s exists; use --force", target)
			}
			if e != nil && !errors.Is(e, fs.ErrNotExist) {
				return nil, e
			}
			policies[target] = b
		}
	}
	type item struct {
		name string
		data []byte
	}
	var items []item
	for _, entry := range entries {
		target := dst
		if directory {
			target = path.Join(dst, strings.TrimPrefix(entry, src+"/"))
		}
		if s.Exists(target) && !force {
			return nil, fmt.Errorf("%s exists; use --force", target)
		}
		if err = s.check(target + ".age"); err != nil {
			return nil, err
		}
		plain, e := s.Read(entry)
		if e != nil {
			return nil, e
		}
		rec, e := s.recipientsWithPolicies(path.Dir(target), policies)
		if e != nil {
			return nil, e
		}
		encrypted, e := s.Crypto.Encrypt(plain, rec)
		clear(plain)
		if e != nil {
			return nil, e
		}
		items = append(items, item{target, encrypted})
	}
	var changed []string
	for p, b := range policies {
		if err = s.writeFile(p, b); err != nil {
			return changed, err
		}
		changed = append(changed, p)
	}
	for _, item := range items {
		if err = s.writeCipher(item.name, item.data); err != nil {
			return changed, fmt.Errorf("copy partially saved: %w", err)
		}
		changed = append(changed, item.name+".age")
	}
	if move {
		for _, entry := range entries {
			if err = s.root.Remove(entry + ".age"); err != nil {
				return changed, fmt.Errorf("destination saved, source removal failed: %w", err)
			}
			changed = append(changed, entry+".age")
			s.prune(path.Dir(entry))
		}
		for p := range originalPolicies {
			if err = s.root.Remove(p); err != nil {
				return changed, err
			}
			changed = append(changed, p)
			s.prune(path.Dir(p))
		}
		if directory {
			s.prune(src)
		}
	}
	return changed, nil
}
