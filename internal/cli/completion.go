package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/jenic/passgage/store"
	"github.com/spf13/cobra"
)

func (a *app) registerCompletions() {
	a.root.CompletionOptions.SetDefaultShellCompDirective(cobra.ShellCompDirectiveNoFileComp)
	a.root.ValidArgsFunction = a.completePaths(1)
	for _, c := range a.root.Commands() {
		switch c.Name() {
		case "show", "insert", "edit", "generate", "rm":
			c.ValidArgsFunction = a.completePaths(1)
		case "cp", "mv":
			c.ValidArgsFunction = a.completePaths(2)
		case "completion":
			c.ValidArgsFunction = func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
				var shells []string
				if len(args) == 0 {
					for _, shell := range []string{"bash", "fish", "powershell", "zsh"} {
						if strings.HasPrefix(shell, prefix) {
							shells = append(shells, shell)
						}
					}
				}
				return shells, cobra.ShellCompDirectiveNoFileComp
			}
		default:
			c.ValidArgsFunction = cobra.NoFileCompletions
		}
		if c.Name() == "reencrypt" {
			if err := c.RegisterFlagCompletionFunc("path", func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
				return a.pathCandidates(prefix, true)
			}); err != nil {
				panic(err) // A duplicate registration is a programming error.
			}
		}
	}
	if err := a.root.RegisterFlagCompletionFunc("allow-age-plugin", cobra.NoFileCompletions); err != nil {
		panic(err)
	}
}

func (a *app) generateCompletion(shell string, out io.Writer) error {
	var script bytes.Buffer
	var err error
	switch shell {
	case "bash":
		return a.root.GenBashCompletionV2(out, true)
	case "zsh":
		return a.root.GenZshCompletion(out)
	case "fish":
		err = a.root.GenFishCompletion(&script, true)
	case "powershell":
		err = a.root.GenPowerShellCompletionWithDesc(&script)
	default:
		return fmt.Errorf("unsupported shell")
	}
	if err != nil {
		return err
	}
	// Cobra v1.10.2's Fish and PowerShell adapters ignore NoFileComp on errors
	// and mishandle escaped spaces in the current token. Adapt only those paths.
	// Check the template anchor so a dependency upgrade cannot silently undo it.
	var old, replacement string
	if shell == "fish" {
		old = `__passgage_debug "Received error directive: aborting."
        # Might as well do file completion, in case it helps
        return 1`
		replacement = `__passgage_debug "Received error directive: aborting."
        if test (math (math --scale 0 $directive / $shellCompDirectiveNoFileComp) % 2) -ne 0
            return 0
        end
        return 1`
	} else {
		old = `__passgage_debug "Received error from custom completion go code"
        return`
		replacement = `__passgage_debug "Received error from custom completion go code"
        if (($Directive -band $ShellCompDirectiveNoFileComp) -ne 0) { "" }
        return`
	}
	patches := [][2]string{{old, replacement}}
	if shell == "fish" {
		patches = append(patches,
			[2]string{`set -l args (commandline -opc)`, `set -l args (string escape -- (commandline -opc))`},
			[2]string{`set -l lastArg (string escape -- (commandline -ct))`, `set -l lastArg (string escape -- (string unescape -- (commandline -ct)))`},
			[2]string{`commandline -t | string escape --style=regex`, `commandline -ct | string unescape | string escape --style=regex`},
		)
	} else {
		patches = append(patches, [2]string{`$WordToComplete = $Arguments.Split(" ")[-1]`, `$CompletionTokens = $null; $CompletionErrors = $null
        $CompletionAst = [System.Management.Automation.Language.Parser]::ParseInput($Command, [ref]$CompletionTokens, [ref]$CompletionErrors)
        $LastElement = $CompletionAst.EndBlock.Statements[-1].PipelineElements[-1].CommandElements[-1]
        if ($LastElement -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
            $WordToComplete = $LastElement.Value
        } else {
            $WordToComplete = $Arguments.Split(" ")[-1]
        }`})
	}
	text := script.String()
	for _, patch := range patches {
		if strings.Count(text, patch[0]) != 1 {
			return fmt.Errorf("unsupported %s completion template", shell)
		}
		text = strings.Replace(text, patch[0], patch[1], 1)
	}
	_, err = io.WriteString(out, text)
	return err
}

func (a *app) completePaths(limit int) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) >= limit {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.pathCandidates(prefix, false)
	}
}

func (a *app) pathCandidates(prefix string, directoriesOnly bool) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	s, err := a.open(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, directive
		}
		return nil, directive | cobra.ShellCompDirectiveError
	}
	defer s.Close()
	dirs, err := s.ListDirectories("")
	if err != nil {
		return nil, directive | cobra.ShellCompDirectiveError
	}
	var entries []string
	if !directoriesOnly {
		entries, err = s.List("")
		if err != nil {
			return nil, directive | cobra.ShellCompDirectiveError
		}
	}
	candidates := make(map[string]bool)
	add := func(name string, directory bool) {
		if _, err := store.Name(name); err != nil || strings.ContainsFunc(name, unicode.IsControl) {
			return
		}
		if directory {
			name += "/"
		}
		if strings.HasPrefix(name, prefix) {
			candidates[name] = true
			if directory {
				directive |= cobra.ShellCompDirectiveNoSpace
			}
		}
	}
	for _, name := range dirs {
		add(name, true)
	}
	for _, name := range entries {
		add(name, false)
	}
	result := make([]string, 0, len(candidates))
	for name := range candidates {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, directive
}
