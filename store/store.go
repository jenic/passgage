// Package store reads and writes passage-compatible age-encrypted stores.
//
// Configuration is explicit: this package does not read passage environment
// settings, interact with a terminal, or perform Git operations. Entry names use
// slash separators and omit the .age suffix. Returned plaintext belongs to the
// caller, who should clear it after use. Mutations are atomic per file, not per
// batch. Locks coordinate cooperating passgage writers, not other programs.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jenic/passgage/agecrypto"
)

// ErrInvalidName reports an unsafe or nonportable logical entry name.
var ErrInvalidName = errors.New("invalid entry name")

// ErrBusy reports a competing writer holding the store lock.
var ErrBusy = errors.New("store is busy; retry after the other operation finishes")

// ErrClosed reports use of a closed store or expired transaction.
var ErrClosed = errors.New("store or transaction is closed")

// OpError describes a failed operation and preserves its underlying error.
type OpError struct {
	Op   string // Operation that failed.
	Name string // Logical name or configured path involved, if any.
	Err  error  // Underlying error, accessible through errors.Is and errors.As.
}

// Error describes the operation and underlying failure.
func (e *OpError) Error() string { return fmt.Sprintf("%s %q: %v", e.Op, e.Name, e.Err) }

// Unwrap returns the underlying failure.
func (e *OpError) Unwrap() error { return e.Err }
func opError(op, name string, err error) error {
	if err == nil {
		return nil
	}
	return &OpError{Op: op, Name: name, Err: err}
}

// Config configures one store. Paths are resolved when Open is called.
type Config struct {
	Dir            string            // Required store directory; no home-directory default is inferred.
	Create         bool              // Create the directory if missing, without generating any keys.
	Crypto         *agecrypto.Engine // Optional engine; nil uses an engine without identities.
	RecipientsFile string            // Highest-precedence recipient override, read on each encryption.
	Recipients     string            // Whitespace-separated recipients, overriding store policies.
}

// Store owns a rooted directory handle. Its operations are safe for concurrent
// use. A Store must be opened with Open and must not be copied.
type Store struct {
	mu sync.Mutex
	fs *filesystem
}

// Open opens a store without loading identities. Close the returned store after use.
func Open(c Config) (*Store, error) {
	if c.Dir == "" {
		return nil, opError("open", "", errors.New("Dir is required"))
	}
	var err error
	c.Dir, err = filepath.Abs(c.Dir)
	if err != nil {
		return nil, opError("open", c.Dir, err)
	}
	if c.RecipientsFile != "" {
		c.RecipientsFile, err = filepath.Abs(c.RecipientsFile)
		if err != nil {
			return nil, opError("open", c.RecipientsFile, err)
		}
	}
	if c.Crypto == nil {
		c.Crypto, err = agecrypto.New(agecrypto.Config{})
		if err != nil {
			return nil, err
		}
	}
	if c.Create {
		if err = os.MkdirAll(c.Dir, 0700); err != nil {
			return nil, opError("open", c.Dir, err)
		}
	}
	root, err := os.OpenRoot(c.Dir)
	if err != nil {
		return nil, opError("open", c.Dir, err)
	}
	return &Store{fs: &filesystem{config: c, crypto: c.Crypto, root: root}}, nil
}

// Close releases the directory handle and references to the crypto engine.
// It waits for running operations and is idempotent. Externally retained engines
// keep their own cached identities for their lifetime.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fs == nil {
		return nil
	}
	err := s.fs.root.Close()
	s.fs = nil
	return opError("close", "", err)
}

// ChangeSet describes completed filesystem mutations, including partial batches.
// Paths are sorted, unique, physical store-relative paths with .age suffixes for
// entries. It does not contain plaintext or imply a transaction was rolled back.
type ChangeSet struct {
	// Paths contains completed changes, including on failure.
	Paths []string
}

// WriteOptions controls replacement of an existing entry.
type WriteOptions struct{ Overwrite bool }

// RemoveOptions controls removal of directory contents and recipient policies.
type RemoveOptions struct{ Recursive bool }

// CopyOptions controls replacement of destination entries and policies.
type CopyOptions struct{ Overwrite bool }

// Tx provides operations under an Update lock. It expires when the callback
// returns. A Tx must not be copied. Use Tx methods inside the callback;
// re-entering its Store deadlocks.
// Callbacks must not recursively invoke a Tx method from a crypto callback.
type Tx struct {
	mu      sync.Mutex
	fs      *filesystem
	changed map[string]struct{}
}

// Update holds the store's writer lock across the callback, including external
// coordination such as a Git commit. Completed changes are returned even when
// the callback fails. It provides no rollback and performs no automatic commit.
func (s *Store) Update(fn func(*Tx) error) (ChangeSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fs == nil {
		return ChangeSet{}, opError("update", "", ErrClosed)
	}
	if fn == nil {
		return ChangeSet{}, opError("update", "", errors.New("nil update callback"))
	}
	unlock, err := s.fs.lock()
	if err != nil {
		return ChangeSet{}, opError("update", "", err)
	}
	defer unlock()
	tx := &Tx{fs: s.fs, changed: make(map[string]struct{})}
	defer func() { tx.mu.Lock(); tx.fs = nil; tx.mu.Unlock() }()
	err = fn(tx)
	// Expire the handle before taking the final snapshot, including operations
	// already started in another goroutine before the callback returned.
	tx.mu.Lock()
	tx.fs = nil
	result := tx.changes()
	tx.mu.Unlock()
	return result, opError("update", "", err)
}

// Changes returns a detached snapshot of completed changes so far.
func (tx *Tx) Changes() ChangeSet { tx.mu.Lock(); defer tx.mu.Unlock(); return tx.changes() }
func (tx *Tx) changes() ChangeSet {
	var paths []string
	for p := range tx.changed {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return ChangeSet{Paths: paths}
}

func readStore[T any](s *Store, op, name string, fn func(*filesystem) (T, error)) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero T
	if s.fs == nil {
		return zero, opError(op, name, ErrClosed)
	}
	v, err := fn(s.fs)
	return v, opError(op, name, err)
}
func readTx[T any](tx *Tx, op, name string, fn func(*filesystem) (T, error)) (T, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	var zero T
	if tx.fs == nil {
		return zero, opError(op, name, ErrClosed)
	}
	v, err := fn(tx.fs)
	return v, opError(op, name, err)
}
func (tx *Tx) mutate(op, name string, fn func(*filesystem) ([]string, error)) (ChangeSet, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.fs == nil {
		return ChangeSet{}, opError(op, name, ErrClosed)
	}
	paths, err := fn(tx.fs)
	for _, p := range paths {
		tx.changed[p] = struct{}{}
	}
	paths = append([]string(nil), paths...)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	return ChangeSet{Paths: paths}, opError(op, name, err)
}

// Read decrypts and fully authenticates an entry before returning plaintext.
func (s *Store) Read(name string) ([]byte, error) {
	return readStore(s, "read", name, func(f *filesystem) ([]byte, error) { return f.read(name) })
}

// Read decrypts and fully authenticates an entry before returning plaintext.
func (s *Tx) Read(name string) ([]byte, error) {
	return readTx(s, "read", name, func(f *filesystem) ([]byte, error) { return f.read(name) })
}

// List returns sorted logical entry names, excluding metadata and symlinks.
func (s *Store) List(prefix string) ([]string, error) {
	return readStore(s, "list", prefix, func(f *filesystem) ([]string, error) { return f.list(prefix, false) })
}

// List returns sorted logical entry names, excluding metadata and symlinks.
func (s *Tx) List(prefix string) ([]string, error) {
	return readTx(s, "list", prefix, func(f *filesystem) ([]string, error) { return f.list(prefix, false) })
}

// ListDirectories returns sorted directories, including empty ones and a nonempty prefix itself.
func (s *Store) ListDirectories(prefix string) ([]string, error) {
	return readStore(s, "listdirectories", prefix, func(f *filesystem) ([]string, error) { return f.list(prefix, true) })
}

// ListDirectories returns sorted directories, including empty ones and a nonempty prefix itself.
func (s *Tx) ListDirectories(prefix string) ([]string, error) {
	return readTx(s, "listdirectories", prefix, func(f *filesystem) ([]string, error) { return f.list(prefix, true) })
}

// Exists reports entry existence, returning errors for invalid paths or failed inspection.
func (s *Store) Exists(name string) (bool, error) {
	return readStore(s, "exists", name, func(f *filesystem) (bool, error) { return f.stat(name, false) })
}

// Exists reports entry existence, returning errors for invalid paths or failed inspection.
func (s *Tx) Exists(name string) (bool, error) {
	return readTx(s, "exists", name, func(f *filesystem) (bool, error) { return f.stat(name, false) })
}

// IsDir reports directory existence; an empty name denotes the store root.
func (s *Store) IsDir(name string) (bool, error) {
	return readStore(s, "isdir", name, func(f *filesystem) (bool, error) { return f.stat(name, true) })
}

// IsDir reports directory existence; an empty name denotes the store root.
func (s *Tx) IsDir(name string) (bool, error) {
	return readTx(s, "isdir", name, func(f *filesystem) (bool, error) { return f.stat(name, true) })
}

// Write encrypts and atomically saves an entry. Replacement requires Overwrite.
func (s *Store) Write(name string, plain []byte, opts WriteOptions) (ChangeSet, error) {
	return s.Update(func(tx *Tx) error { _, err := tx.Write(name, plain, opts); return err })
}

// Write encrypts and atomically saves an entry. Replacement requires Overwrite.
func (tx *Tx) Write(name string, plain []byte, opts WriteOptions) (ChangeSet, error) {
	return tx.mutate("write", name, func(f *filesystem) ([]string, error) {
		n, err := Name(name)
		if err != nil {
			return nil, err
		}
		exists, err := f.stat(n, false)
		if err != nil {
			return nil, err
		}
		if exists && !opts.Overwrite {
			return nil, os.ErrExist
		}
		if err = f.write(n, plain); err != nil {
			return nil, err
		}
		return []string{n + ".age"}, nil
	})
}

// Remove removes an entry, or a subtree when Recursive is set.
func (s *Store) Remove(name string, opts RemoveOptions) (ChangeSet, error) {
	return s.Update(func(tx *Tx) error { _, err := tx.Remove(name, opts); return err })
}

// Remove removes an entry, or a subtree when Recursive is set.
func (tx *Tx) Remove(name string, opts RemoveOptions) (ChangeSet, error) {
	return tx.mutate("remove", name, func(f *filesystem) ([]string, error) { return f.remove(name, opts.Recursive) })
}

// Copy copies entries and policies, encrypting for destination recipients.
func (s *Store) Copy(src, dst string, opts CopyOptions) (ChangeSet, error) {
	return s.Update(func(tx *Tx) error { _, err := tx.Copy(src, dst, opts); return err })
}

// Copy copies entries and policies, encrypting for destination recipients.
func (tx *Tx) Copy(src, dst string, opts CopyOptions) (ChangeSet, error) {
	return tx.mutate("copy", src, func(f *filesystem) ([]string, error) { return f.copy(src, dst, false, opts.Overwrite) })
}

// Move copies to destination recipients before deleting source entries and policies.
func (s *Store) Move(src, dst string, opts CopyOptions) (ChangeSet, error) {
	return s.Update(func(tx *Tx) error { _, err := tx.Move(src, dst, opts); return err })
}

// Move copies to destination recipients before deleting source entries and policies.
func (tx *Tx) Move(src, dst string, opts CopyOptions) (ChangeSet, error) {
	return tx.mutate("move", src, func(f *filesystem) ([]string, error) { return f.copy(src, dst, true, opts.Overwrite) })
}

// Reencrypt applies current recipient policies. Empty prefix selects the entire store.
func (s *Store) Reencrypt(prefix string) (ChangeSet, error) {
	return s.Update(func(tx *Tx) error { _, err := tx.Reencrypt(prefix); return err })
}

// Reencrypt applies current recipient policies. Empty prefix selects the entire store.
func (tx *Tx) Reencrypt(prefix string) (ChangeSet, error) {
	return tx.mutate("reencrypt", prefix, func(f *filesystem) ([]string, error) {
		names, err := f.list(prefix, false)
		if err != nil {
			return nil, err
		}
		var changed []string
		for _, name := range names {
			plain, err := f.read(name)
			if err != nil {
				return changed, err
			}
			err = f.write(name, plain)
			clear(plain)
			if err != nil {
				return changed, err
			}
			changed = append(changed, name+".age")
		}
		return changed, nil
	})
}
