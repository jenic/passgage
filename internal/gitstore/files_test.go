package gitstore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreGitFilesAndIgnoreRules(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		".gitignore":             "custom-rule\n.passgage.lock\n",
		".age-recipients":        "root recipient",
		"nested/.age-recipients": "nested recipient",
		"nested/entry.age":       "ciphertext fixture",
		"ordinary.txt":           "unrelated",
		".hidden/entry.age":      "hidden fixture",
		".passgage.lock":         "",
	} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := SetupGitIgnore(dir); err != nil {
			t.Fatal(err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "custom-rule\n.passgage.lock\n.passgage-*\n" {
		t.Fatalf("ignore rules: %q", contents)
	}
	if err := os.Symlink(filepath.Join(dir, "nested", "entry.age"), filepath.Join(dir, "link.age")); err != nil {
		t.Logf("symlink unavailable: %v", err)
	}
	files, err := GitFiles(dir)
	if err != nil || !reflect.DeepEqual(files, []string{".age-recipients", ".gitignore", "nested/.age-recipients", "nested/entry.age"}) {
		t.Fatalf("Git paths: %q, %v", files, err)
	}
}

func TestGitIgnoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".gitignore")); err != nil {
		t.Skip(err)
	}
	if err := SetupGitIgnore(dir); err == nil {
		t.Fatal("accepted symlink ignore file")
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "keep" {
		t.Fatalf("outside file changed: %q, %v", content, err)
	}
}
