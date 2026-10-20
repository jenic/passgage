//go:build completion

package completion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type shellFixture struct {
	dir, script, shell string
}

func command(t *testing.T, dir, executable string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %q: %v\n%s\n%s", executable, args, err, out, &stderr)
	}
	return string(out)
}

func fixture(t *testing.T, shell string) shellFixture {
	t.Helper()
	binary := os.Getenv("PASSGAGE_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("PASSGAGE_BINARY must name an absolute built binary")
	}
	executable := shell
	if shell == "powershell" {
		executable = "pwsh"
	}
	shellPath, err := exec.LookPath(executable)
	if err != nil {
		t.Fatalf("requested completion shell %s is unavailable: %v", shell, err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin with spaces")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	name := "passgage"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, key := range []string{"PASSAGE_RECIPIENTS", "PASSAGE_RECIPIENTS_FILE", "BASH_COMP_DEBUG_FILE"} {
		t.Setenv(key, "")
	}
	t.Setenv("PASSAGE_DIR", filepath.Join(dir, "store"))
	t.Setenv("PASSAGE_IDENTITIES_FILE", filepath.Join(dir, "no-identities"))
	t.Setenv("PASSWORD_STORE_GENERATED_LENGTH", "25")
	t.Setenv("PASSWORD_STORE_CLIP_TIME", "45")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("ZDOTDIR", dir)
	t.Setenv("BASH_COMPLETION_USER_FILE", "/dev/null")
	t.Setenv("TERM", "xterm")
	t.Setenv("LC_ALL", "C.UTF-8")
	for _, folder := range []string{"email/nested", "empty", "space folder"} {
		if err := os.MkdirAll(filepath.Join(os.Getenv("PASSAGE_DIR"), folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []string{"email/work", "email/personal", "email/nested/login", "space entry", "space folder/work", "quote'entry", "literal $value;name", "日本語"} {
		if err := os.WriteFile(filepath.Join(os.Getenv("PASSAGE_DIR"), entry+".age"), []byte("invalid ciphertext"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("not a store entry"), 0600); err != nil {
		t.Fatal(err)
	}
	suffix := "." + shell
	if shell == "powershell" {
		suffix = ".ps1"
	}
	script := filepath.Join(dir, "completion"+suffix)
	if err := os.WriteFile(script, []byte(command(t, dir, binary, "completion", shell)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PASSGAGE_COMPLETION_SCRIPT", script)
	return shellFixture{dir, script, shellPath}
}

func TestShellCompletion(t *testing.T) {
	shells := os.Getenv("PASSGAGE_COMPLETION_SHELLS")
	if shells == "" {
		shells = "bash,zsh,fish,powershell"
	}
	for _, shell := range strings.Split(shells, ",") {
		shell = strings.TrimSpace(shell)
		t.Run(shell, func(t *testing.T) {
			switch shell {
			case "bash", "zsh":
				testInteractive(t, shell, fixture(t, shell))
			case "fish", "powershell":
				testCompletionAPI(t, shell, fixture(t, shell))
			default:
				t.Fatalf("unsupported completion test shell %q", shell)
			}
		})
	}
}

func testCompletionAPI(t *testing.T, shell string, f shellFixture) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"passgage email/w", []string{"email/work"}},
		{"passgage show ", []string{"email/", "email/nested/", "email/nested/login", "email/personal", "email/work", "empty/", "literal $value;name", "quote'entry", "space entry", "space folder/", "space folder/work", "日本語"}},
		{"passgage ls email/w", []string{"email/work"}},
		{"passgage show --clip=2 email/w", []string{"email/work"}},
		{"passgage show emp", []string{"empty/"}},
		{"passgage reencrypt --path emp", []string{"empty/"}},
		{"passgage cp email/work email/p", []string{"email/personal"}},
		{`passgage cp "space folder/work" email/w`, []string{"email/work"}},
		{"passgage show email/n", []string{"email/nested/", "email/nested/login"}},
		{"passgage generate email/work unrelated", nil},
		{"passgage grep unrelated", nil},
		{"passgage show unrelated", nil},
		{"passgage vers", []string{"version"}},
		{"passgage show spa", []string{"space entry", "space folder/", "space folder/work"}},
		{"passgage show quote", []string{"quote'entry"}},
		{"passgage show lit", []string{"literal $value;name"}},
		{"passgage show 日本", []string{"日本語"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Setenv("PASSGAGE_COMPLETION_QUERY", tc.line)
			var got []string
			if shell == "fish" {
				output := command(t, f.dir, f.shell, "--no-config", "-c", `source "$PASSGAGE_COMPLETION_SCRIPT"; complete -C "$PASSGAGE_COMPLETION_QUERY"`)
				for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
					if line != "" {
						name, _, _ := strings.Cut(line, "\t")
						got = append(got, name)
					}
				}
			} else {
				output := command(t, f.dir, f.shell, "-NoProfile", "-NonInteractive", "-Command", powershellQuery)
				var matches []struct{ CompletionText, ListItemText string }
				if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &matches); err != nil {
					t.Fatalf("PowerShell completion JSON: %v\n%s", err, output)
				}
				for _, match := range matches {
					if match.CompletionText != "" {
						got = append(got, match.ListItemText)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("completion = %q; want %q", got, tc.want)
			}
		})
	}
	t.Run("continue a path containing spaces", func(t *testing.T) {
		line := `passgage show space\ folder/w`
		if shell == "powershell" {
			line = "passgage show space` folder/w"
		}
		t.Setenv("PASSGAGE_COMPLETION_QUERY", line)
		if shell == "fish" {
			output := command(t, f.dir, f.shell, "--no-config", "-c", `source "$PASSGAGE_COMPLETION_SCRIPT"; complete -C "$PASSGAGE_COMPLETION_QUERY"`)
			if strings.TrimSpace(output) != "space folder/work" {
				t.Fatalf("path continuation: %q", output)
			}
		} else {
			output := command(t, f.dir, f.shell, "-NoProfile", "-NonInteractive", "-Command", powershellQuery)
			if !strings.Contains(output, `"ListItemText":"space folder/work"`) {
				t.Fatalf("path continuation: %s", output)
			}
		}
	})
	t.Run("store error has no file fallback", func(t *testing.T) {
		t.Setenv("PASSAGE_DIR", filepath.Join(f.dir, "unrelated.txt"))
		t.Setenv("PASSGAGE_COMPLETION_QUERY", "passgage show unrelated")
		if shell == "fish" {
			if output := command(t, f.dir, f.shell, "--no-config", "-c", `source "$PASSGAGE_COMPLETION_SCRIPT"; complete -C "$PASSGAGE_COMPLETION_QUERY"; true`); strings.TrimSpace(output) != "" {
				t.Fatalf("unexpected file fallback: %q", output)
			}
		} else {
			output := command(t, f.dir, f.shell, "-NoProfile", "-NonInteractive", "-Command", powershellQuery)
			if strings.TrimSpace(output) != "[]" {
				t.Fatalf("unexpected file fallback: %s", output)
			}
		}
	})
}

const powershellQuery = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
. $env:PASSGAGE_COMPLETION_SCRIPT
Import-Module PSReadLine
Set-PSReadLineKeyHandler -Key Tab -Function MenuComplete
$line = $env:PASSGAGE_COMPLETION_QUERY
$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseInput($line, [ref]$tokens, [ref]$errors)
$commandAst = $ast.EndBlock.Statements[0].PipelineElements[0]
$word = if ($line.EndsWith(' ')) { '' } else { $commandAst.CommandElements[-1].Value }
$raw = @(& $__passgageCompleterBlock $word $commandAst $line.Length)
# Cobra emits an empty string to suppress native filename fallback. PSReadLine
# handles this sentinel, but CompleteInput throws when converting it to a result.
# Check that contract directly for no matches; use the public API for real results.
if ($raw.Count -eq 1 -and $raw[0] -is [string] -and $raw[0] -eq '') {
    $completionMatches = @()
} else {
    $completionMatches = @([System.Management.Automation.CommandCompletion]::CompleteInput($line, $line.Length, $null).CompletionMatches | Where-Object CompletionText)
}
foreach ($match in $completionMatches) {
    if ($line -match 'show (spa|quote|lit|日本)$') {
        $tokens = $null; $errors = $null
        $ast = [System.Management.Automation.Language.Parser]::ParseInput(('passgage show ' + $match.CompletionText), [ref]$tokens, [ref]$errors)
        $statements = $ast.EndBlock.Statements
        if ($errors.Count -or $statements.Count -ne 1 -or $statements[0].PipelineElements.Count -ne 1) { throw 'Unsafe completion syntax' }
        $arguments = $statements[0].PipelineElements[0].CommandElements
        if ($arguments.Count -ne 3 -or $arguments[2].Value -cne $match.ListItemText) { throw 'Completion did not preserve the literal entry name' }
    }
}
ConvertTo-Json -Compress -InputObject @($completionMatches | Select-Object CompletionText, ListItemText)
`
