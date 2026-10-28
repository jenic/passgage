package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"empty", "team/nested", ".git/private", "team/.hidden"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "entry.age"), []byte("not ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(Config{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"", []string{"empty", "team", "team/nested"}},
		{"team", []string{"team", "team/nested"}},
		{"empty", []string{"empty"}},
	} {
		got, err := s.ListDirectories(tc.prefix)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ListDirectories(%q) = %q, %v; want %q", tc.prefix, got, err, tc.want)
		}
	}
	for _, prefix := range []string{"missing", "entry.age", "../outside", ".git"} {
		if _, err := s.ListDirectories(prefix); err == nil {
			t.Errorf("accepted invalid prefix %q", prefix)
		}
	}
	if _, err := s.List(""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListDirectories(""); err == nil {
		t.Fatal("listing a closed store succeeded")
	}
}

func TestListDirectoriesExcludesSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Skip(err)
	}
	s, err := Open(Config{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if dirs, err := s.ListDirectories(""); err != nil || len(dirs) != 0 {
		t.Fatalf("symlink directories: %q, %v", dirs, err)
	}
	if _, err := s.ListDirectories("outside"); err == nil {
		t.Fatal("accepted a symlink prefix")
	}
}
