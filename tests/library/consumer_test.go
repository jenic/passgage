// Package library tests consumption from a genuinely separate Go module.
package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExternalConsumer(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source, err := os.ReadFile(filepath.Join("testdata", "consumer", "consumer_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	module := fmt.Sprintf("module example.com/passgage-consumer\n\ngo 1.26.0\n\nrequire github.com/jenic/passgage v0.0.0\nreplace github.com/jenic/passgage => %s\n", strconv.Quote(filepath.ToSlash(root)))
	for name, data := range map[string][]byte{"go.mod": []byte(module), "consumer_test.go": source} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=go1.27.1", "CGO_ENABLED=0", "GOCACHE="+filepath.Join(root, ".gocache"), "GOMODCACHE="+filepath.Join(root, ".gomodcache"), "GOPATH="+filepath.Join(root, ".cache", "go"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	// Dependency module vendor trees are ignored by Go. Prepare the consumer's
	// own vendor tree using only cached, verified modules, with networking disabled.
	// Resolve only consumer dependencies, without fetching dependency test suites.
	run("list", "-mod=mod", "-deps", "-test", ".")
	run("mod", "verify")
	run("mod", "vendor")
	deps := run("list", "-mod=vendor", "-deps", "github.com/jenic/passgage/store", "github.com/jenic/passgage/agecrypto")
	for _, path := range strings.Fields(deps) {
		if strings.HasPrefix(path, "github.com/jenic/passgage/internal/") && path != "github.com/jenic/passgage/internal/storefs" {
			t.Errorf("public library imports internal integration: %s", path)
		}
		for _, prefix := range []string{"github.com/spf13/cobra", "github.com/go-git/", "charm.land/", "golang.design/x/clipboard", "github.com/skip2/go-qrcode"} {
			if strings.HasPrefix(path, prefix) {
				t.Errorf("public library imports CLI dependency: %s", path)
			}
		}
	}
	run("test", "-mod=vendor", "./...")
}
