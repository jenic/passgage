package gitstore

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/jenic/passgage/internal/storefs"
)

// GitFiles returns entries and recipient policies suitable for an initial commit.
func GitFiles(dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var names []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".age") || d.Name() == ".age-recipients" || p == ".gitignore" {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

// SetupGitIgnore preserves existing rules and excludes internal coordination files.
func SetupGitIgnore(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	if err := storefs.Check(root, ".gitignore"); err != nil {
		return err
	}
	b, err := root.ReadFile(".gitignore")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(b)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	for _, rule := range []string{".passgage.lock", ".passgage-*"} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			if line == rule {
				found = true
			}
		}
		if !found {
			text += rule + "\n"
		}
	}
	return storefs.WriteFile(root, ".gitignore", []byte(text))
}
