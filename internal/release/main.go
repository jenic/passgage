// Command release builds reproducible platform archives using the Go toolchain.
// It is a development tool, not part of the passgage runtime.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	version := flag.String("version", "dev", "Release version")
	verify := flag.Bool("verify", false, "Build twice and compare binaries")
	flag.Parse()
	if err := run(*version, *verify); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(version string, verify bool) error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	var sums bytes.Buffer
	for _, target := range []struct{ os, arch string }{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		suffix := ""
		if target.os == "windows" {
			suffix = ".exe"
		}
		binary := "passgage" + suffix
		dir, err := os.MkdirTemp("dist", "build-")
		if err != nil {
			return err
		}
		build := func(p string) error {
			cmd := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid= -X github.com/jenic/passgage/internal/build.Version="+version, "-o", p, "./cmd/passgage")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		p := filepath.Join(dir, binary)
		if err = build(p); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if verify {
			if err = build(p); err != nil {
				return err
			}
			other, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, other) {
				return fmt.Errorf("nonreproducible %s/%s", target.os, target.arch)
			}
		}
		var archive bytes.Buffer
		type member struct {
			name string
			data []byte
			mode os.FileMode
		}
		members := []member{{binary, b, 0755}}
		for _, p := range []string{"COPYING", "README.md"} {
			data, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			members = append(members, member{p, data, 0644})
		}
		name := "passgage_" + target.os + "_" + target.arch
		if target.os == "windows" {
			name += ".zip"
			z := zip.NewWriter(&archive)
			for _, m := range members {
				h := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
				h.SetMode(m.mode)
				h.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
				w, err := z.CreateHeader(h)
				if err != nil {
					return err
				}
				if _, err = w.Write(m.data); err != nil {
					return err
				}
			}
			if err = z.Close(); err != nil {
				return err
			}
		} else {
			name += ".tar.gz"
			g := gzip.NewWriter(&archive)
			t := tar.NewWriter(g)
			for _, m := range members {
				if err = t.WriteHeader(&tar.Header{Name: m.name, Mode: int64(m.mode), Size: int64(len(m.data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
					return err
				}
				if _, err = t.Write(m.data); err != nil {
					return err
				}
			}
			if err = t.Close(); err != nil {
				return err
			}
			if err = g.Close(); err != nil {
				return err
			}
		}
		if err = os.WriteFile(filepath.Join("dist", name), archive.Bytes(), 0644); err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(archive.Bytes()), name)
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Println(name)
	}
	return os.WriteFile("dist/SHA256SUMS", sums.Bytes(), 0644)
}
