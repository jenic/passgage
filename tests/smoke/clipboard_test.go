//go:build desktop

package smoke

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/internal/store"
	clipboard "golang.design/x/clipboard"
)

func TestClipboardProcessLifecycle(t *testing.T) {
	binary := os.Getenv("PASSGAGE_BINARY")
	if binary == "" {
		t.Fatal("use make test-desktop")
	}
	if e := clipboard.Init(); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	t.Setenv("PASSGAGE_CACHE_DIR", filepath.Join(dir, "cache"))
	c := cfg.Config{Dir: filepath.Join(dir, "store"), Identities: filepath.Join(dir, "identities")}
	if e := store.Initialize(c, ""); e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(c, false, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Write("fixture", []byte("passgage clipboard test\n")); e != nil {
		t.Fatal(e)
	}
	copy := func() {
		t.Helper()
		cmd := exec.Command(binary, "show", "--clip", "fixture")
		cmd.Env = append(os.Environ(), "PASSAGE_DIR="+c.Dir, "PASSAGE_IDENTITIES_FILE="+c.Identities, "PASSWORD_STORE_CLIP_TIME=2", "PASSWORD_STORE_X_SELECTION=clipboard", "PATH=")
		cmd.Stderr = os.Stderr
		start := time.Now()
		if _, e := cmd.Output(); e != nil {
			t.Fatal(e)
		}
		if time.Since(start) > 1500*time.Millisecond {
			t.Fatal("foreground waited for expiry")
		}
	}
	read := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b, e := clipboard.Read(ctx, clipboard.FmtText)
		if errors.Is(e, clipboard.ErrNoData) {
			return ""
		}
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	copy()
	if read() != "passgage clipboard test" {
		t.Fatal("clipboard did not survive foreground exit")
	}
	time.Sleep(2500 * time.Millisecond)
	if got := read(); got == "passgage clipboard test" {
		t.Fatal("secret did not expire")
	}
	copy()
	if _, e = clipboard.Write(context.Background(), clipboard.FmtText, []byte("new content")); e != nil {
		t.Fatal(e)
	}
	time.Sleep(2500 * time.Millisecond)
	if read() != "new content" {
		t.Fatal("expiry overwrote another application's clipboard")
	}
	copy()
	time.Sleep(1200 * time.Millisecond)
	copy()
	time.Sleep(1100 * time.Millisecond)
	if read() != "passgage clipboard test" {
		t.Fatal("old helper cleared identical newer copy")
	}
	time.Sleep(1400 * time.Millisecond)
	if read() == "passgage clipboard test" {
		t.Fatal("new copy did not expire")
	}
}
