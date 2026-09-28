// Package cfg resolves passage-compatible environment settings.
package cfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the effective configuration for one invocation.
type Config struct {
	Dir, Identities, RecipientsFile, Recipients, Characters, NoSymbols, Selection string
	ClipTime                                                                      time.Duration
	Length                                                                        int
	Plugins                                                                       []string
}

// Load reads environment settings without requiring a store or identity.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	c := Config{Dir: value("PASSAGE_DIR", filepath.Join(home, ".passage", "store")), Identities: value("PASSAGE_IDENTITIES_FILE", filepath.Join(home, ".passage", "identities")), RecipientsFile: os.Getenv("PASSAGE_RECIPIENTS_FILE"), Recipients: os.Getenv("PASSAGE_RECIPIENTS"), Characters: value("PASSWORD_STORE_CHARACTER_SET", "[:punct:][:alnum:]"), NoSymbols: value("PASSWORD_STORE_CHARACTER_SET_NO_SYMBOLS", "[:alnum:]"), Selection: value("PASSWORD_STORE_X_SELECTION", "clipboard"), Plugins: strings.FieldsFunc(os.Getenv("PASSGAGE_AGE_PLUGINS"), func(r rune) bool { return r == ',' })}
	n, err := strconv.Atoi(value("PASSWORD_STORE_GENERATED_LENGTH", "25"))
	if err != nil || n < 1 || n > 1048576 {
		return c, fmt.Errorf("invalid PASSWORD_STORE_GENERATED_LENGTH")
	}
	c.Length = n
	n, err = strconv.Atoi(value("PASSWORD_STORE_CLIP_TIME", "45"))
	if err != nil || n < 1 || n > 86400 {
		return c, fmt.Errorf("PASSWORD_STORE_CLIP_TIME must be 1..86400 seconds")
	}
	c.ClipTime = time.Duration(n) * time.Second
	c.Dir, err = filepath.Abs(c.Dir)
	if err != nil {
		return c, err
	}
	c.Identities, err = filepath.Abs(c.Identities)
	return c, err
}
func value(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
