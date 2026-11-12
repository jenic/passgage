//go:build terminal && (linux || darwin)

package terminal_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/jenic/passgage/store"
)

const testPIN = "synthetic-pin-7492"
const testPassword = "synthetic-restic-password\n"

// The copied test executable speaks the real age protocol without a shell or
// hardware. Interaction happens in Wrap/Unwrap, as required by the protocol.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "--age-plugin=") && os.Getenv("PASSGAGE_TERMINAL_PLUGIN") == "1" {
		if err := os.WriteFile(os.Getenv("PASSGAGE_PLUGIN_STARTED"), []byte("started"), 0600); err != nil {
			os.Exit(1)
		}
		p, err := plugin.New("terminaltest")
		if err != nil {
			os.Exit(1)
		}
		p.HandleIdentity(func(data []byte) (age.Identity, error) {
			id, err := age.ParseX25519Identity(string(data))
			return &interactiveIdentity{Identity: id, ui: p}, err
		})
		p.HandleRecipient(func(data []byte) (age.Recipient, error) {
			r, err := age.ParseX25519Recipient(string(data))
			return &interactiveRecipient{Recipient: r, ui: p}, err
		})
		os.Exit(p.Main())
	}
	os.Exit(m.Run())
}

type interactiveIdentity struct {
	age.Identity
	ui *plugin.Plugin
}

func (id *interactiveIdentity) Unwrap(stanzas []*age.Stanza) ([]byte, error) {
	if err := interact(id.ui); err != nil {
		return nil, err
	}
	return id.Identity.Unwrap(stanzas)
}

type interactiveRecipient struct {
	age.Recipient
	ui *plugin.Plugin
}

func (r *interactiveRecipient) Wrap(key []byte) ([]*age.Stanza, error) {
	if err := interact(r.ui); err != nil {
		return nil, err
	}
	return r.Recipient.Wrap(key)
}

func interact(p *plugin.Plugin) error {
	if err := p.DisplayMessage("synthetic hardware token"); err != nil {
		return err
	}
	switch os.Getenv("PASSGAGE_PLUGIN_INTERACTION") {
	case "public":
		value, err := p.RequestValue("Synthetic account", false)
		if err != nil {
			return err
		}
		if value != "synthetic-account" {
			return errors.New("incorrect account")
		}
	case "confirm", "acknowledge":
		no := "Deny"
		if os.Getenv("PASSGAGE_PLUGIN_INTERACTION") == "acknowledge" {
			no = ""
		}
		yes, err := p.Confirm("Synthetic approval", "Allow", no)
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("approval denied")
		}
	default:
		// Control characters must be sanitized by age's terminal UI.
		value, err := p.RequestValue("Synthetic PIN \x1b[31m", true)
		if err != nil {
			return err
		}
		if value != testPIN {
			return errors.New("incorrect synthetic PIN")
		}
	}
	return nil
}

func pluginFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	s := fixture(t)
	if _, err := s.Write("Backups/key", []byte(testPassword), store.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	// Keep a native reader for checking ciphertext written by the plugin. The
	// engine loads identities lazily, before the CLI's identity file is replaced.
	if _, err := s.Read("Backups/key"); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PASSAGE_IDENTITIES_FILE")
	identity, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(identity))
	if err != nil || len(ids) != 1 {
		t.Fatalf("fixture identity: %v", err)
	}
	id := ids[0].(*age.X25519Identity)
	encoded := plugin.EncodeIdentity("terminaltest", []byte(id.String()))
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(os.Getenv("PATH"), "age-plugin-terminaltest"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PASSGAGE_TERMINAL_PLUGIN", "1")
	t.Setenv("PASSGAGE_PLUGIN_STARTED", filepath.Join(t.TempDir(), "started"))
	t.Setenv("PASSGAGE_AGE_PLUGINS", "terminaltest")
	t.Setenv("AGEDEBUG", "")
	t.Setenv("PASSGAGE_TERMINAL_CAPTURE", "1")
	t.Setenv("PASSGAGE_TERMINAL_INPUT", "")
	return s, plugin.EncodeRecipient("terminaltest", []byte(id.Recipient().String()))
}

func TestPluginTerminalInteraction(t *testing.T) {
	pluginFixture(t)
	for _, tc := range []struct {
		name, mode, prompt, keys, stdin, diagnostic string
		fail                                        bool
	}{
		{name: "restic", prompt: "Synthetic PIN", keys: testPIN + "\n"},
		{name: "piped-stdin", prompt: "Synthetic PIN", keys: testPIN + "\n", stdin: "wrong-stdin-pin\n"},
		{name: "wrong-pin", prompt: "Synthetic PIN", keys: "wrong-pin\n", fail: true, diagnostic: "incorrect synthetic PIN"},
		{name: "public-eof", mode: "public", prompt: "Synthetic account", keys: "\x04", fail: true, diagnostic: "EOF"},
		{name: "public", mode: "public", prompt: "Synthetic account", keys: "synthetic-accounX\x7ft\n"},
		{name: "confirm-yes", mode: "confirm", prompt: "press [1]", keys: "1"},
		{name: "confirm-no", mode: "confirm", prompt: "press [1]", keys: "2", fail: true, diagnostic: "approval denied"},
		{name: "confirm-invalid", mode: "confirm", prompt: "press [1]", keys: "x1", diagnostic: "invalid selection"},
		{name: "confirm-cancel", mode: "confirm", prompt: "press [1]", keys: "\x03", fail: true, diagnostic: "user cancelled prompt"},
		{name: "acknowledge", mode: "acknowledge", prompt: "press enter", keys: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PASSGAGE_PLUGIN_INTERACTION", tc.mode)
			t.Setenv("PASSGAGE_TERMINAL_INPUT", tc.stdin)
			terminal, r := sessionWithPrompt(t, []string{"Backups/key"}, tc.keys, "", tc.prompt)
			if (r.Code != 0) != tc.fail {
				t.Fatalf("exit %d, stderr %q", r.Code, r.Stderr)
			}
			want := testPassword
			if tc.fail {
				want = ""
			}
			if r.Stdout != want {
				t.Fatalf("unexpected password output: %q", r.Stdout)
			}
			if !strings.Contains(r.Stderr, "terminaltest: synthetic hardware token\n") || !strings.Contains(r.Stderr, tc.diagnostic) {
				t.Fatalf("missing diagnostic: %q", r.Stderr)
			}
			if strings.Contains(r.Stderr, tc.prompt) && !tc.fail {
				t.Fatalf("input prompt leaked to stderr: %q", r.Stderr)
			}
			for _, output := range []string{terminal, r.Stdout, r.Stderr} {
				if strings.Contains(output, testPIN) || strings.Contains(output, "\x1b[31m") || strings.Contains(output, "wrong-stdin-pin") {
					t.Fatal("secret input or unsanitized prompt leaked")
				}
			}
		})
	}
}

func TestPluginTerminalEncryption(t *testing.T) {
	s, recipient := pluginFixture(t)
	t.Setenv("PASSAGE_RECIPIENTS", recipient)
	t.Setenv("PASSGAGE_PLUGIN_INTERACTION", "pin")
	t.Setenv("PASSGAGE_TERMINAL_INPUT", "synthetic new entry\n")
	terminal, r := sessionWithPrompt(t, []string{"insert", "--multiline", "Backups/new"}, testPIN+"\n", "", "Synthetic PIN")
	if r.Code != 0 || r.Stdout != "" || strings.Contains(terminal+r.Stderr, testPIN) {
		t.Fatalf("insert failed: exit %d, stderr %q", r.Code, r.Stderr)
	}
	plain, err := s.Read("Backups/new")
	if err != nil || string(plain) != "synthetic new entry\n" {
		t.Fatalf("inserted entry: %q, %v", plain, err)
	}
}

func detachedCommand(t *testing.T, input string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("PASSGAGE_BINARY"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatal("detached command timed out")
	}
	return stdout.String(), stderr.String(), err
}

func TestPluginWithoutTerminal(t *testing.T) {
	pluginFixture(t)
	for _, mode := range []string{"pin", "public", "confirm", "acknowledge"} {
		for _, input := range []struct{ name, value string }{{"null", ""}, {"pipe", testPIN + "\n"}} {
			t.Run(mode+"/"+input.name, func(t *testing.T) {
				t.Setenv("PASSGAGE_PLUGIN_INTERACTION", mode)
				stdout, stderr, err := detachedCommand(t, input.value, "Backups/key")
				if err == nil || stdout != "" || !strings.Contains(stderr, "standard input is not a terminal") || strings.Contains(stderr, testPIN) {
					t.Fatalf("detached prompt: stdout %q, stderr %q, error %v", stdout, stderr, err)
				}
			})
		}
	}
}

func TestPluginConsentAndLazyInteraction(t *testing.T) {
	pluginFixture(t)
	marker := os.Getenv("PASSGAGE_PLUGIN_STARTED")
	for _, args := range [][]string{{"--help"}, {"show", "Backups"}, {"__complete", "Backups/"}} {
		if _, stderr, err := detachedCommand(t, "", args...); err != nil {
			t.Fatalf("%v: %v, %s", args, err, stderr)
		}
	}
	t.Setenv("PASSGAGE_AGE_PLUGINS", "")
	stdout, stderr, err := detachedCommand(t, "", "Backups/key")
	if err == nil || stdout != "" || !strings.Contains(stderr, "age plugin is not allowed") {
		t.Fatalf("plugin consent: %q, %q, %v", stdout, stderr, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin launched without need or consent: %v", err)
	}
}
