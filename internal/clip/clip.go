// Package clip serves expiring secrets using native desktop clipboard APIs.
package clip

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	clipboard "golang.design/x/clipboard"
)

// Request is sent only over a private pipe to the helper.
type Request struct {
	Text      []byte
	Duration  time.Duration
	Selection string
}

// Copy starts this executable as a detached helper and waits for publication.
func Copy(text []byte, duration time.Duration, selection string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "__clipboard")
	detach(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	if err = json.NewEncoder(in).Encode(Request{text, duration, selection}); err != nil {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	_ = in.Close()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ok\n" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("clipboard helper failed: %s", line)
		}
		_ = out.Close()
		return cmd.Process.Release()
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("clipboard helper timed out")
	}
}

// Backend abstracts clipboard ownership for lifecycle tests.
type Backend interface {
	Write([]byte) (<-chan struct{}, error)
	Read() ([]byte, error)
}
type native struct {
	opts   []clipboard.Option
	marker []byte
}

func (n native) Write(b []byte) (<-chan struct{}, error) {
	if len(b) == 0 {
		return clipboard.Write(context.Background(), clipboard.FmtText, b, n.opts...)
	}
	opts := append([]clipboard.Option{}, n.opts...)
	opts = append(opts, clipboard.Item{Format: clipboard.FmtText, Bytes: b}, clipboard.Item{Format: clipboard.Register("application/x-passgage-generation"), Bytes: n.marker})
	return clipboard.WriteAll(context.Background(), opts...)
}
func (n native) Read() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	marker, err := clipboard.Read(ctx, clipboard.Register("application/x-passgage-generation"), n.opts...)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(marker, n.marker) {
		return nil, nil
	}
	return clipboard.Read(ctx, clipboard.FmtText, n.opts...)
}

// Serve clears only an unchanged, still-owned clipboard after its deadline.
// The generation guard serializes publication and expiry among passgage helpers.
func Serve(b Backend, r Request, ready func() error, lock func() (func(), error), current func(string) (bool, error)) error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		unlock()
		return err
	}
	generation := hex.EncodeToString(token[:])
	changed, err := b.Write(r.Text)
	if err != nil {
		unlock()
		return err
	}
	if _, err = current("set:" + generation); err != nil {
		unlock()
		return err
	}
	unlock()
	if err = ready(); err != nil {
		return err
	}
	timer := time.NewTimer(r.Duration)
	defer timer.Stop()
	select {
	case <-changed:
		return nil
	case <-timer.C:
	}
	unlock, err = lock()
	if err != nil {
		return err
	}
	defer unlock()
	ok, err := current(generation)
	if err != nil || !ok {
		return err
	}
	select {
	case <-changed:
		return nil
	default:
	}
	now, err := b.Read()
	if err != nil {
		return err
	}
	defer clear(now)
	if !bytes.Equal(now, r.Text) {
		return nil
	}
	select {
	case <-changed:
		return nil
	default:
	}
	_, err = b.Write([]byte{})
	return err
}

// Helper runs the hidden clipboard worker. It never reads a password store.
func Helper(in io.Reader, out io.Writer) error {
	var r Request
	if err := json.NewDecoder(io.LimitReader(in, 16<<20)).Decode(&r); err != nil {
		return err
	}
	defer clear(r.Text)
	if r.Duration <= 0 || r.Duration > 24*time.Hour {
		return fmt.Errorf("invalid clipboard duration")
	}
	if r.Selection != "clipboard" && r.Selection != "primary" {
		return fmt.Errorf("unsupported clipboard selection")
	}
	if r.Selection == "primary" && (runtime.GOOS == "windows" || runtime.GOOS == "darwin") {
		return fmt.Errorf("primary selection is unavailable on this OS")
	}
	if err := clipboard.Init(); err != nil {
		return err
	}
	marker := make([]byte, 32)
	if _, err := rand.Read(marker); err != nil {
		return err
	}
	n := native{marker: marker}
	if r.Selection == "primary" {
		n.opts = []clipboard.Option{clipboard.FromPrimary()}
	}
	dir := os.Getenv("PASSGAGE_CACHE_DIR")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(base, "passgage")
	}
	var err error
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Separate displays and selections must not supersede one another.
	session := []byte(os.Getenv("DISPLAY") + "/" + os.Getenv("WAYLAND_DISPLAY") + "/" + r.Selection)
	// Encode rather than interpolate display strings into a path.
	name := hex.EncodeToString(session)
	if len(name) > 180 {
		name = name[:180]
	}
	l := flock.New(filepath.Join(dir, "clipboard-"+name+".lock"))
	defer l.Close()
	lock := func() (func(), error) {
		if err := l.Lock(); err != nil {
			return nil, err
		}
		return func() { _ = l.Unlock() }, nil
	}
	state := filepath.Join(dir, "clipboard-"+name+".generation")
	current := func(v string) (bool, error) {
		if len(v) > 4 && v[:4] == "set:" {
			return true, os.WriteFile(state, []byte(v[4:]), 0600)
		}
		b, err := os.ReadFile(state)
		return string(b) == v, err
	}
	return Serve(n, r, func() error {
		_, err := fmt.Fprintln(out, "ok")
		if f, ok := out.(*os.File); ok {
			_ = f.Close()
		}
		return err
	}, lock, current)
}
