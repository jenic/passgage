//go:build completion && (linux || darwin)

package completion_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func testInteractive(t *testing.T, shell string, f shellFixture) {
	t.Helper()
	var setup string
	var args []string
	if shell == "bash" {
		dependency := os.Getenv("PASSGAGE_BASH_COMPLETION")
		if dependency == "" {
			dependency = "/usr/share/bash-completion/bash_completion"
		}
		if _, err := os.Stat(dependency); err != nil {
			t.Fatalf("install bash-completion or set PASSGAGE_BASH_COMPLETION: %v", err)
		}
		t.Setenv("PASSGAGE_BASH_COMPLETION", dependency)
		setup = `
source "$PASSGAGE_BASH_COMPLETION"
source "$PASSGAGE_COMPLETION_SCRIPT"
PS1=''; PS2=''; PROMPT_COMMAND=''
bind 'set enable-bracketed-paste off'
capture_buffer() {
    printf '\n__PASSGAGE_BUFFER__%s__PASSGAGE_END__\n' "$READLINE_LINE"
    READLINE_LINE=''; READLINE_POINT=0
}
bind -x '"\C-x\C-t": capture_buffer'
printf '\n__PASSGAGE_READY__\n'
`
		args = []string{"--noprofile", "--rcfile", filepath.Join(f.dir, "setup"), "-i"}
	} else {
		setup = `
if [[ -n $PASSGAGE_ZSH_MODULE_PATH ]]; then module_path=("$PASSGAGE_ZSH_MODULE_PATH" $module_path); fi
if [[ -n $PASSGAGE_ZSH_FUNCTIONS ]]; then fpath=("$PASSGAGE_ZSH_FUNCTIONS"/**/*(N/) $fpath); fi
autoload -Uz compinit
compinit -D -u
source "$PASSGAGE_COMPLETION_SCRIPT"
PS1=''; PS2=''; RPS1=''
bindkey -e
capture_buffer() {
    print -r -- "__PASSGAGE_BUFFER__${BUFFER}__PASSGAGE_END__"
    BUFFER=''; CURSOR=0
    zle reset-prompt
}
zle -N capture_buffer
bindkey '^X^T' capture_buffer
printf '\n__PASSGAGE_READY__\n'
`
		args = []string{"-d", "-i"}
		if err := os.WriteFile(filepath.Join(f.dir, ".zshrc"), []byte(setup), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "setup"), []byte(setup), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, f.shell, args...)
	cmd.Dir = f.dir
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 160})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		terminal.Close()
		cancel()
		cmd.Wait()
	})
	chunks := make(chan string, 128)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 8192)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				select {
				case chunks <- string(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	until := func(t *testing.T, marker string) string {
		t.Helper()
		var output strings.Builder
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("shell exited waiting for %s: %q", marker, output.String())
				}
				output.WriteString(chunk)
				if strings.Contains(output.String(), marker) {
					return output.String()
				}
			case <-timer.C:
				t.Fatalf("timeout waiting for %s: %q", marker, output.String())
			}
		}
	}
	until(t, "__PASSGAGE_READY__")
	for _, tc := range []struct{ line, want string }{
		{"passgage email/w", "passgage email/work "},
		{"passgage ls email/w", "passgage ls email/work "},
		{"passgage -c2 email/w", "passgage -c2 email/work "},
		{"passgage show emp", "passgage show empty/"},
		{"passgage reencrypt --path emp", "passgage reencrypt --path empty/"},
		{"passgage show email/n", "passgage show email/nested/"},
		{"passgage cp email/work email/p", "passgage cp email/work email/personal "},
		{"passgage generate email/work unrelated", "passgage generate email/work unrelated"},
		{"passgage grep unrelated", "passgage grep unrelated"},
		{"passgage show unrelated", "passgage show unrelated"},
		{"passgage vers", "passgage version "},
		{"passgage show space\\ e", `passgage show space\ entry `},
		{"passgage show space\\ folder/w", `passgage show space\ folder/work `},
		{"passgage show quote", `passgage show quote\'entry `},
		{"passgage show lit", `passgage show literal\ \$value\;name `},
		{"passgage show 日本", "passgage show 日本語 "},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if _, err := fmt.Fprint(terminal, tc.line+"\t\x18\x14"); err != nil {
				t.Fatal(err)
			}
			output := until(t, "__PASSGAGE_END__")
			_, buffer, ok := strings.Cut(output, "__PASSGAGE_BUFFER__")
			buffer, _, _ = strings.Cut(buffer, "__PASSGAGE_END__")
			if !ok || buffer != tc.want {
				t.Fatalf("completed buffer = %q; want %q\nterminal: %q", buffer, tc.want, output)
			}
		})
	}
}
