package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/jenic/passgage/agecrypto"
	"github.com/jenic/passgage/internal/storefs"
)

type filesystem struct {
	config Config
	crypto *agecrypto.Engine
	root   *os.Root
}

// Name validates portable logical entry paths.
func Name(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00:\r\n") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	name = strings.TrimSuffix(name, "/")
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") || strings.ContainsAny(p, "<>\"|?*") {
			return "", fmt.Errorf("%w: unsafe or nonportable path %q", ErrInvalidName, name)
		}
		base := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", fmt.Errorf("%w: reserved Windows name %q", ErrInvalidName, p)
		}
	}
	return name, nil
}
func (s *filesystem) check(name string) error { return storefs.Check(s.root, name) }

// Lock serializes cooperating mutations. The caller must release the lock.
func (s *filesystem) lock() (func(), error) {
	p := filepath.Join(s.config.Dir, ".passgage.lock")
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
		return nil, ErrBusy
	}
	return func() { _ = l.Unlock(); _ = l.Close() }, nil
}

// Read decrypts and fully authenticates an entry before returning any plaintext.
func (s *filesystem) read(name string) ([]byte, error) {
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
	return s.crypto.Decrypt(b)
}

func (s *filesystem) stat(name string, directory bool) (bool, error) {
	if name == "" && directory {
		return true, nil
	}
	n, err := Name(name)
	if err != nil {
		return false, err
	}
	if !directory {
		n += ".age"
	}
	if err = s.check(n); err != nil {
		return false, err
	}
	fi, err := s.root.Lstat(n)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if directory {
		return fi.IsDir(), nil
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("not a regular entry: %s", n)
	}
	return true, nil
}

func (s *filesystem) list(prefix string, directories bool) ([]string, error) {
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
		if directories {
			if d.IsDir() && p != "." {
				names = append(names, p)
			} else if p == base && !d.IsDir() {
				return fmt.Errorf("not a store directory: %s", p)
			}
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
func (s *filesystem) write(name string, plain []byte) error {
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
	b, err := s.crypto.Encrypt(plain, rec)
	if err != nil {
		return err
	}
	return s.writeCipher(n, b)
}
func (s *filesystem) writeCipher(n string, b []byte) error {
	return s.writeFile(n+".age", b)
}
func (s *filesystem) writeFile(n string, b []byte) error { return storefs.WriteFile(s.root, n, b) }
func (s *filesystem) recipients(dir string) (string, error) {
	return s.recipientsWithPolicies(dir, nil)
}
func (s *filesystem) recipientsWithPolicies(dir string, policies map[string][]byte) (string, error) {
	if s.config.RecipientsFile != "" {
		b, err := os.ReadFile(s.config.RecipientsFile)
		return string(b) + "\n", err
	}
	if s.config.Recipients != "" {
		return strings.Join(strings.Fields(s.config.Recipients), "\n") + "\n", nil
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

func (s *filesystem) policies(prefix string) (map[string][]byte, error) {
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
func (s *filesystem) prune(dir string) {
	for dir != "." && dir != "" {
		if err := s.root.Remove(dir); err != nil {
			return
		}
		dir = path.Dir(dir)
	}
}

// Remove deletes entries and their subtree recipient policies, preserving other files.
func (s *filesystem) remove(name string, recursive bool) ([]string, error) {
	n, err := Name(name)
	if err != nil {
		return nil, err
	}
	if err = s.check(n); err != nil {
		return nil, err
	}
	exists, err := s.stat(n, false)
	if err != nil {
		return nil, err
	}
	if exists {
		err = s.root.Remove(n + ".age")
		if err != nil {
			return nil, err
		}
		s.prune(path.Dir(n))
		return []string{n + ".age"}, err
	}
	directory, err := s.stat(n, true)
	if err != nil {
		return nil, err
	}
	if !directory {
		return nil, fs.ErrNotExist
	}
	if !recursive {
		return nil, fmt.Errorf("directory removal requires Recursive")
	}
	entries, err := s.list(n, false)
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
func (s *filesystem) copy(src, dst string, move, force bool) ([]string, error) {
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
	exists, err := s.stat(src, false)
	if err != nil {
		return nil, err
	}
	directory := !exists
	if directory {
		entries, err = s.list(src, false)
		if err != nil {
			return nil, err
		}
	} else {
		entries = []string{src}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries to copy")
	}
	isDir, err := s.stat(dst, true)
	if err != nil {
		return nil, err
	}
	if isDir || trailing {
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
				return nil, fmt.Errorf("recipient policy %s: %w", target, fs.ErrExist)
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
		exists, e := s.stat(target, false)
		if e != nil {
			return nil, e
		}
		if exists && !force {
			return nil, fmt.Errorf("%s: %w", target, fs.ErrExist)
		}
		if err = s.check(target + ".age"); err != nil {
			return nil, err
		}
		plain, e := s.read(entry)
		if e != nil {
			return nil, e
		}
		rec, e := s.recipientsWithPolicies(path.Dir(target), policies)
		if e != nil {
			clear(plain)
			return nil, e
		}
		encrypted, e := s.crypto.Encrypt(plain, rec)
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
