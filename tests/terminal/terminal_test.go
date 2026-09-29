//go:build terminal && (linux || darwin)

package terminal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/internal/store"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type result struct {
	Code     int
	Restored bool
	Residual []byte
	Error    string
}

// The helper owns the controlling terminal before and after the real CLI.
// Its post-exit read is diagnostic only, never part of the shipped application.
func TestTerminalChild(t *testing.T) {
	if os.Getenv("PASSGAGE_TERMINAL_CHILD") != "1" {
		return
	}
	r := result{}
	before, err := term.GetState(0)
	if err != nil {
		panic(err)
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PASSGAGE_TERMINAL_ARGS")), &args); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	cmd := exec.CommandContext(ctx, os.Getenv("PASSGAGE_BINARY"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	cancel()
	if err != nil {
		if cmd.ProcessState == nil {
			r.Code = -1
			r.Error = err.Error()
		} else {
			r.Code = cmd.ProcessState.ExitCode()
		}
	}
	after, err := term.GetState(0)
	r.Restored = err == nil && reflect.DeepEqual(before, after)
	if _, err = term.MakeRaw(0); err == nil {
		fds := []unix.PollFd{{Fd: 0, Events: unix.POLLIN}}
		if n, e := unix.Poll(fds, 200); e != nil {
			r.Error = e.Error()
		} else if n > 0 {
			var b [4096]byte
			if n, e := unix.Read(0, b[:]); e == nil {
				r.Residual = b[:n]
			} else {
				r.Error = e.Error()
			}
		}
		_ = term.Restore(0, before)
	} else {
		r.Error = err.Error()
	}
	f := os.NewFile(3, "result")
	_ = json.NewEncoder(f).Encode(r)
	_ = f.Close()
	os.Exit(0)
}

func fixture(t *testing.T) *store.Store {
	t.Helper()
	binary := os.Getenv("PASSGAGE_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("PASSGAGE_BINARY must name the absolute built binary")
	}
	dir := t.TempDir()
	t.Setenv("PASSAGE_DIR", filepath.Join(dir, "store"))
	t.Setenv("PASSAGE_IDENTITIES_FILE", filepath.Join(dir, "identities"))
	for _, k := range []string{"PASSAGE_RECIPIENTS", "PASSAGE_RECIPIENTS_FILE", "PASSGAGE_AGE_PLUGINS", "CI", "NO_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "COLORTERM"} {
		t.Setenv(k, "")
	}
	t.Setenv("PASSGAGE_EDITOR_THEME", "dark")
	t.Setenv("TERM", "rxvt-unicode-256color")
	t.Setenv("PATH", t.TempDir())
	c, err := cfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(c, ""); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(c, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Write("folder/entry", []byte("synthetic\nmetadata\n\n")); err != nil {
		t.Fatal(err)
	}
	return s
}

func session(t *testing.T, args []string, keys, response string) (string, result) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	defer wp.Close()
	encoded, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalChild$")
	cmd.Env = append(os.Environ(), "PASSGAGE_TERMINAL_CHILD=1", "PASSGAGE_TERMINAL_ARGS="+string(encoded))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.ExtraFiles = []*os.File{wp}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	_ = wp.Close()
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var b [4096]byte
		for {
			n, err := master.Read(b[:])
			if n > 0 {
				select {
				case chunks <- string(b[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var out strings.Builder
	var sent, replied bool
	err = collectTerminalOutput(ctx, chunks, done, func(chunk string) {
		out.WriteString(chunk)
		text := out.String()
		if !replied && strings.Contains(text, "\x1b[6n") {
			_, _ = io.WriteString(master, response)
			replied = true
		}
		if !sent && keys != "" && strings.Contains(text, "Ctrl+S save") {
			_, _ = io.WriteString(master, keys)
			sent = true
		}
	}, func() { _ = slave.Close() })
	if err != nil {
		t.Fatalf("terminal session: %v; output %q", err, out.String())
	}
	var r result
	if err := json.NewDecoder(rp).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if !r.Restored || len(r.Residual) > 0 || r.Error != "" {
		t.Fatalf("terminal state: %+v", r)
	}
	text := out.String()
	if strings.Contains(text, "\x1b]10;?") || strings.Contains(text, "\x1b]11;?") || strings.Contains(text, "\x1b[6n") {
		t.Fatalf("unexpected color/cursor query: %q", text)
	}
	return text, r
}

// PTY closure and cmd.Wait are independent events. Wait for both, accepting
// either order, and keep the timeout active while draining final output.
func collectTerminalOutput(ctx context.Context, chunks <-chan string, done <-chan error, output func(string), exited func()) error {
	for chunks != nil || done != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			output(chunk)
		case err := <-done:
			exited()
			done = nil
			if err != nil {
				return fmt.Errorf("terminal helper: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestCollectTerminalOutput(t *testing.T) {
	for _, outputFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("output-first=%v", outputFirst), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			chunks := make(chan string)
			done := make(chan error)
			exited := make(chan struct{})
			finished := make(chan error, 1)
			var out strings.Builder
			go func() {
				finished <- collectTerminalOutput(ctx, chunks, done, func(s string) { out.WriteString(s) }, func() { close(exited) })
			}()
			sendChunk := func(s string) {
				t.Helper()
				select {
				case chunks <- s:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			sendChunk("initial")
			if outputFirst {
				close(chunks)
				select {
				case err := <-finished:
					t.Fatalf("returned before process completion: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
			}
			select {
			case done <- nil:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-exited:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			want := "initial"
			if !outputFirst {
				sendChunk("final")
				close(chunks)
				want += "final"
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestCollectTerminalOutputErrors(t *testing.T) {
	t.Run("helper failure after output closes", func(t *testing.T) {
		chunks := make(chan string)
		close(chunks)
		done := make(chan error, 1)
		want := errors.New("helper failed")
		done <- want
		err := collectTerminalOutput(context.Background(), chunks, done, func(string) {}, func() {})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
	for _, outputFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("timeout/output-first=%v", outputFirst), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			chunks := make(chan string)
			done := make(chan error, 1)
			if outputFirst {
				close(chunks)
			} else {
				done <- nil
			}
			err := collectTerminalOutput(ctx, chunks, done, func(string) {}, func() {})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want deadline exceeded", err)
			}
		})
	}
}

func TestOrdinaryCommandsNeverQuery(t *testing.T) {
	fixture(t)
	for _, rgba := range []bool{false, true} {
		for _, terminator := range []string{"\a", "\x1b\\"} {
			for _, reversed := range []bool{false, true} {
				color := "\x1b]11;rgb:1111/2222/3333" + terminator
				if rgba {
					color = "\x1b]11;rgba:1111/2222/3333/aaaa" + terminator
				}
				reply := color + "\x1b[1;1R"
				if reversed {
					reply = "\x1b[1;1R" + color
				}
				t.Run(fmt.Sprintf("rgba=%v/terminator=%q/reverse=%v", rgba, terminator, reversed), func(t *testing.T) {
					out, r := session(t, []string{"version"}, "", reply)
					if r.Code != 0 || !strings.HasPrefix(out, "passgage ") || strings.Contains(out, "\x1b") {
						t.Fatalf("version: %q %+v", out, r)
					}
				})
			}
		}
	}
	t.Setenv("PASSGAGE_EDITOR_THEME", "invalid-but-irrelevant")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "Portable age-backed password store"},
		{[]string{"show", "folder"}, "folder\r\n└── entry\r\n"},
		{[]string{"show", "folder/entry"}, "synthetic\r\nmetadata\r\n\r\n"},
	} {
		out, r := session(t, tc.args, "", "")
		if r.Code != 0 || !strings.Contains(out, tc.want) || strings.Contains(out, "\x1b") {
			t.Fatalf("%v: %q %+v", tc.args, out, r)
		}
	}
}

func TestEditorTerminalLifecycle(t *testing.T) {
	s := fixture(t)
	for _, tc := range []struct {
		name, keys string
		save       bool
	}{
		{"save", "\x1b[200~日本語\n\n\x1b[201~\x13", true},
		{"escape", "\x1b", false},
		{"ctrl-c", "\x03", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(filepath.Join(s.Config.Dir, "folder", "entry.age"))
			if err != nil {
				t.Fatal(err)
			}
			out, r := session(t, []string{"edit", "folder/entry"}, tc.keys, "")
			if !strings.Contains(out, "\x1b[?1049h") || !strings.Contains(out, "\x1b[?1049l") {
				t.Fatal("alternate screen not entered/restored")
			}
			if tc.save {
				b, err := s.Read("folder/entry")
				if r.Code != 0 || err != nil || string(b) != "synthetic\nmetadata\n\n日本語\n\n" {
					t.Fatalf("save: %q %v %+v", b, err, r)
				}
			} else {
				after, err := os.ReadFile(filepath.Join(s.Config.Dir, "folder", "entry.age"))
				if r.Code == 0 || err != nil || !bytes.Equal(before, after) {
					t.Fatalf("cancel modified entry: %v %+v", err, r)
				}
			}
		})
	}
}

func TestInvalidThemeDoesNotEnterTerminal(t *testing.T) {
	s := fixture(t)
	t.Setenv("PASSGAGE_EDITOR_THEME", "auto")
	file := filepath.Join(s.Config.Dir, "folder", "entry.age")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out, r := session(t, []string{"edit", "folder/entry"}, "", "")
	after, err := os.ReadFile(file)
	if err != nil || r.Code == 0 || !bytes.Equal(before, after) || strings.Contains(out, "\x1b") || !strings.Contains(out, "PASSGAGE_EDITOR_THEME") {
		t.Fatalf("invalid theme: %q %+v %v", out, r, err)
	}
}
