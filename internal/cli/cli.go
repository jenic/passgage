// Package cli implements the passgage command interface.
package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/jenic/passgage/internal/build"
	"github.com/jenic/passgage/internal/cfg"
	"github.com/jenic/passgage/internal/clip"
	"github.com/jenic/passgage/internal/editor"
	"github.com/jenic/passgage/internal/gitstore"
	"github.com/jenic/passgage/internal/store"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type app struct {
	root    *cobra.Command
	plugins []string
	input   *bufio.Reader
}

// New builds a command tree with injectable streams for testing.
func New(in io.Reader, out, errout io.Writer) *cobra.Command {
	a := &app{input: bufio.NewReader(in)}
	root := &cobra.Command{Use: "passgage", Version: build.Version, Short: "Portable age-backed password store", SilenceUsage: true, SilenceErrors: true, Args: cobra.MaximumNArgs(1)}
	a.root = root
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errout)
	root.PersistentFlags().StringSliceVar(&a.plugins, "allow-age-plugin", nil, "Explicitly allow named age plugins")
	a.show(root)
	show := &cobra.Command{Use: "show [entry|directory]", Aliases: []string{"ls", "list"}, Short: "Show an entry or list entries", Args: cobra.MaximumNArgs(1)}
	a.show(show)
	root.AddCommand(show)
	root.AddCommand(&cobra.Command{Use: "version", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, e := fmt.Fprintln(c.OutOrStdout(), "passgage "+build.Version)
		return e
	}})
	var protect bool
	init := &cobra.Command{Use: "init", Short: "Create a native age identity and store", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		conf, e := a.config()
		if e != nil {
			return e
		}
		pass := ""
		if protect {
			pass, e = a.secret("New identity passphrase: ")
			if e != nil {
				return e
			}
			again, e := a.secret("Confirm passphrase: ")
			if e != nil {
				return e
			}
			if pass == "" || pass != again {
				return fmt.Errorf("passphrases are empty or do not match")
			}
		}
		if e = store.Initialize(conf, pass); e != nil {
			return e
		}
		fmt.Fprintln(c.ErrOrStderr(), "Created identity and store at", conf.Dir)
		return nil
	}}
	init.Flags().BoolVar(&protect, "passphrase", false, "Encrypt the identity with a passphrase")
	root.AddCommand(init)
	a.insert()
	a.generate()
	a.edit()
	a.remove()
	a.copyMove(false)
	a.copyMove(true)
	a.reencrypt()
	a.search()
	a.grep()
	a.git()
	comp := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(c.OutOrStdout(), true)
		case "zsh":
			return root.GenZshCompletion(c.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(c.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletionWithDesc(c.OutOrStdout())
		default:
			return fmt.Errorf("unsupported shell")
		}
	}}
	root.AddCommand(comp)
	for _, c := range root.Commands() {
		if c.Name() != "git" && c.Name() != "completion" {
			c.ValidArgsFunction = a.complete
		}
	}
	return root
}

// Execute runs the CLI. Only the hidden helper bypasses configuration loading.
func Execute(args []string) error {
	if len(args) > 0 && args[0] == "__clipboard" {
		err := clip.Helper(os.Stdin, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stdout, err)
		}
		return err
	}
	r := New(os.Stdin, os.Stdout, os.Stderr)
	r.SetArgs(NormalizeArgs(args))
	return r.Execute()
}

// NormalizeArgs preserves passage's attached optional line-number shorthand.
func NormalizeArgs(args []string) []string {
	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		arg := out[i]
		if arg == "--" {
			break
		}
		if arg == "--allow-age-plugin" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "show", "ls", "list":
		case "init", "find", "search", "grep", "insert", "add", "edit", "generate", "reencrypt", "rm", "remove", "delete", "cp", "copy", "mv", "move", "rename", "git", "help", "version", "completion":
			return out
		}
		break
	}
	for i, arg := range out {
		if arg == "--" {
			break
		}
		if len(arg) > 2 && arg[0] == '-' && (arg[1] == 'c' || arg[1] == 'q') {
			if _, err := strconv.ParseUint(arg[2:], 10, 64); err == nil {
				out[i] = arg[:2] + "=" + arg[2:]
			}
		}
	}
	return out
}
func (a *app) config() (cfg.Config, error) {
	c, e := cfg.Load()
	c.Plugins = append(c.Plugins, a.plugins...)
	return c, e
}
func (a *app) secret(prompt string) (string, error) {
	f, ok := a.root.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", fmt.Errorf("%s requires a terminal", strings.TrimSpace(prompt))
	}
	fmt.Fprint(a.root.ErrOrStderr(), prompt)
	b, e := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.root.ErrOrStderr())
	return string(b), e
}
func (a *app) confirm(prompt string, force bool) error {
	if force {
		return nil
	}
	f, ok := a.root.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return fmt.Errorf("%s; use --force for noninteractive changes", prompt)
	}
	fmt.Fprint(a.root.ErrOrStderr(), prompt+" [y/N] ")
	line, e := a.input.ReadString('\n')
	if e != nil {
		return e
	}
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return fmt.Errorf("canceled")
	}
	return nil
}
func (a *app) open(create bool) (*store.Store, error) {
	c, e := a.config()
	if e != nil {
		return nil, e
	}
	return store.Open(c, create, a.secret)
}
func (a *app) mutate(fn func(*store.Store) ([]string, string, error)) error {
	s, e := a.open(true)
	if e != nil {
		return e
	}
	defer s.Close()
	unlock, e := s.Lock()
	if e != nil {
		return e
	}
	defer unlock()
	paths, message, e := fn(s)
	if e != nil {
		if len(paths) > 0 {
			return fmt.Errorf("some files were changed (%s), without an automatic commit: %w", strings.Join(paths, ", "), e)
		}
		return e
	}
	if e = gitstore.AutoCommit(s.Config.Dir, paths, message, a.root.ErrOrStderr()); e != nil {
		return fmt.Errorf("files saved, but automatic Git commit failed: %w", e)
	}
	return nil
}
func (a *app) show(c *cobra.Command) {
	var clipLine, qrLine string
	c.Flags().StringVarP(&clipLine, "clip", "c", "", "Copy selected line (default 1)")
	c.Flags().Lookup("clip").NoOptDefVal = "1"
	c.Flags().StringVarP(&qrLine, "qrcode", "q", "", "Show selected line as a terminal QR code")
	c.Flags().Lookup("qrcode").NoOptDefVal = "1"
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if clipLine != "" && qrLine != "" {
			return fmt.Errorf("--clip and --qrcode are mutually exclusive")
		}
		s, e := a.open(false)
		if e != nil {
			return e
		}
		defer s.Close()
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		if name == "" || (!s.Exists(name) && s.IsDir(name)) {
			if clipLine != "" || qrLine != "" {
				return fmt.Errorf("select an entry to copy or encode")
			}
			names, e := s.List(name)
			if e != nil {
				return e
			}
			return writeListing(cmd.OutOrStdout(), name, names)
		}
		b, e := s.Read(name)
		if e != nil {
			return e
		}
		defer clear(b)
		if clipLine == "" && qrLine == "" {
			_, e = cmd.OutOrStdout().Write(b)
			return e
		}
		line := clipLine
		if line == "" {
			line = qrLine
		}
		n, e := strconv.Atoi(line)
		if e != nil || n < 1 {
			return fmt.Errorf("line number must be positive")
		}
		lines := bytes.Split(b, []byte{'\n'})
		if n > len(lines) || len(lines[n-1]) == 0 {
			return fmt.Errorf("no password at line %d", n)
		}
		return a.outputSecret(s, lines[n-1], name, clipLine != "", qrLine != "", false)
	}
}
func (a *app) outputSecret(s *store.Store, b []byte, name string, copy, qr, print bool) error {
	if copy {
		if e := clip.Copy(b, s.Config.ClipTime, s.Config.Selection); e != nil {
			return e
		}
		fmt.Fprintf(a.root.ErrOrStderr(), "Copied %s to clipboard. Will clear in %s.\n", name, s.Config.ClipTime)
		return nil
	}
	if qr {
		q, e := qrcode.New(string(b), qrcode.Medium)
		if e != nil {
			return e
		}
		fmt.Fprint(a.root.OutOrStdout(), q.ToSmallString(false))
		return nil
	}
	if print {
		fmt.Fprintln(a.root.OutOrStdout(), string(b))
	}
	return nil
}
func (a *app) insert() {
	var multi, echo, force bool
	c := &cobra.Command{Use: "insert <entry>", Aliases: []string{"add"}, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return a.mutate(func(s *store.Store) ([]string, string, error) {
			name, err := store.Name(args[0])
			if err != nil {
				return nil, "", err
			}
			if multi && echo {
				return nil, "", fmt.Errorf("--multiline and --echo are mutually exclusive")
			}
			if s.Exists(name) {
				if e := a.confirm("Overwrite "+name+"?", force); e != nil {
					return nil, "", e
				}
			}
			var b []byte
			var e error
			if multi {
				b, e = io.ReadAll(a.input)
			} else if echo {
				line, err := a.input.ReadString('\n')
				e = err
				if errors.Is(e, io.EOF) && line != "" {
					e = nil
				}
				b = []byte(strings.TrimSuffix(line, "\n") + "\n")
			} else {
				var p, q string
				p, e = a.secret("Password: ")
				if e == nil {
					q, e = a.secret("Confirm password: ")
				}
				if e == nil && p != q {
					e = fmt.Errorf("passwords do not match")
				}
				b = []byte(p + "\n")
			}
			if e != nil {
				return nil, "", e
			}
			defer clear(b)
			e = s.Write(name, b)
			if e != nil {
				return nil, "", e
			}
			return []string{name + ".age"}, "Add given password for " + name + " to store.", e
		})
	}}
	c.Flags().BoolVarP(&multi, "multiline", "m", false, "Read complete contents from stdin")
	c.Flags().BoolVarP(&echo, "echo", "e", false, "Read one visible line from stdin")
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite without prompting")
	a.root.AddCommand(c)
}
func (a *app) generate() {
	var noSymbols, copy, qr, inplace, force bool
	c := &cobra.Command{Use: "generate <entry> [length]", Args: cobra.RangeArgs(1, 2), RunE: func(c *cobra.Command, args []string) error {
		if (copy && qr) || (inplace && force) {
			return fmt.Errorf("incompatible generation flags")
		}
		var generated []byte
		var conf cfg.Config
		e := a.mutate(func(s *store.Store) ([]string, string, error) {
			conf = s.Config
			name, nameErr := store.Name(args[0])
			if nameErr != nil {
				return nil, "", nameErr
			}
			length := conf.Length
			if len(args) == 2 {
				n, e := strconv.Atoi(args[1])
				if e != nil {
					return nil, "", e
				}
				length = n
			}
			if !inplace && s.Exists(name) {
				if e := a.confirm("Overwrite "+name+"?", force); e != nil {
					return nil, "", e
				}
			}
			pattern := conf.Characters
			if noSymbols {
				pattern = conf.NoSymbols
			}
			var err error
			generated, err = store.Generate(length, pattern)
			if err != nil {
				return nil, "", err
			}
			b := append(append([]byte{}, generated...), '\n')
			verb := "Add"
			if inplace {
				old, e := s.Read(name)
				if e != nil {
					return nil, "", e
				}
				if i := bytes.IndexByte(old, '\n'); i >= 0 {
					b = append(b, old[i+1:]...)
				}
				clear(old)
				verb = "Replace"
			}
			defer clear(b)
			err = s.Write(name, b)
			if err != nil {
				return nil, "", err
			}
			return []string{name + ".age"}, verb + " generated password for " + name + ".", err
		})
		defer clear(generated)
		if e != nil {
			return e
		}
		return a.outputSecret(&store.Store{Config: conf}, generated, args[0], copy, qr, true)
	}}
	c.Flags().BoolVarP(&noSymbols, "no-symbols", "n", false, "Use the alphanumeric character set")
	c.Flags().BoolVarP(&copy, "clip", "c", false, "Copy the generated password")
	c.Flags().BoolVarP(&qr, "qrcode", "q", false, "Render a QR code")
	c.Flags().BoolVarP(&inplace, "in-place", "i", false, "Replace only the first line")
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite without prompting")
	a.root.AddCommand(c)
}
func (a *app) edit() {
	var external string
	var editorArgs []string
	c := &cobra.Command{Use: "edit <entry>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return a.mutate(func(s *store.Store) ([]string, string, error) {
			name, err := store.Name(args[0])
			if err != nil {
				return nil, "", err
			}
			var old []byte
			var e error
			verb := "Add"
			if s.Exists(name) {
				old, e = s.Read(name)
				if e != nil {
					return nil, "", e
				}
				verb = "Edit"
			}
			defer clear(old)
			var b []byte
			if external != "" {
				b, e = editor.External(old, external, editorArgs)
			} else {
				b, e = editor.Edit(old)
			}
			if e != nil {
				return nil, "", e
			}
			defer clear(b)
			if bytes.Equal(old, b) {
				return nil, "", nil
			}
			e = s.Write(name, b)
			if e != nil {
				return nil, "", e
			}
			return []string{name + ".age"}, verb + " password for " + name + " using passgage.", e
		})
	}}
	c.Flags().StringVar(&external, "external", "", "Explicit external editor executable")
	c.Flags().StringArrayVar(&editorArgs, "editor-arg", nil, "Argument passed directly to the external editor (repeatable)")
	a.root.AddCommand(c)
}
func (a *app) remove() {
	var recursive, force bool
	c := &cobra.Command{Use: "rm <entry|directory>", Aliases: []string{"remove", "delete"}, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return a.mutate(func(s *store.Store) ([]string, string, error) {
			if e := a.confirm("Delete "+args[0]+"?", force); e != nil {
				return nil, "", e
			}
			p, e := s.Remove(args[0], recursive)
			return p, "Remove " + args[0] + " from store.", e
		})
	}}
	c.Flags().BoolVarP(&recursive, "recursive", "r", false, "Remove a directory's entries")
	c.Flags().BoolVarP(&force, "force", "f", false, "Do not prompt")
	a.root.AddCommand(c)
}
func (a *app) copyMove(move bool) {
	var force bool
	name := "cp"
	aliases := []string{"copy"}
	verb := "Copy"
	if move {
		name = "mv"
		aliases = []string{"rename", "move"}
		verb = "Rename"
	}
	c := &cobra.Command{Use: name + " <source> <destination>", Aliases: aliases, Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		return a.mutate(func(s *store.Store) ([]string, string, error) {
			p, e := s.Copy(args[0], args[1], move, force)
			return p, verb + " " + args[0] + " to " + args[1] + ".", e
		})
	}}
	c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite destinations")
	a.root.AddCommand(c)
}
func (a *app) reencrypt() {
	var prefix string
	c := &cobra.Command{Use: "reencrypt", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return a.mutate(func(s *store.Store) ([]string, string, error) {
			names, e := s.List(prefix)
			if e != nil {
				return nil, "", e
			}
			var changed []string
			for _, n := range names {
				b, e := s.Read(n)
				if e != nil {
					return changed, "", e
				}
				e = s.Write(n, b)
				clear(b)
				if e != nil {
					return changed, "", e
				}
				changed = append(changed, n+".age")
				fmt.Fprintln(c.ErrOrStderr(), n+": reencrypted")
			}
			return changed, "Reencrypted " + prefix + ".", nil
		})
	}}
	c.Flags().StringVarP(&prefix, "path", "p", "", "Restrict to a directory")
	a.root.AddCommand(c)
}
func (a *app) search() {
	c := &cobra.Command{Use: "find <term>...", Aliases: []string{"search"}, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := a.open(false)
		if e != nil {
			return e
		}
		defer s.Close()
		names, e := s.List("")
		if e != nil {
			return e
		}
		for _, n := range names {
			for _, term := range args {
				if strings.Contains(strings.ToLower(n), strings.ToLower(term)) {
					fmt.Fprintln(c.OutOrStdout(), n)
					break
				}
			}
		}
		return nil
	}}
	a.root.AddCommand(c)
}
func (a *app) grep() {
	var insensitive, numbers, files, invert, fixed, extended bool
	c := &cobra.Command{Use: "grep [flags] <pattern>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		pattern := args[0]
		if fixed {
			pattern = regexp.QuoteMeta(pattern)
		}
		if insensitive {
			pattern = "(?i)" + pattern
		}
		re, e := regexp.Compile(pattern)
		if e != nil {
			return e
		}
		s, e := a.open(false)
		if e != nil {
			return e
		}
		defer s.Close()
		names, e := s.List("")
		if e != nil {
			return e
		}
		found := false
		for _, n := range names {
			b, e := s.Read(n)
			if e != nil {
				return fmt.Errorf("decrypt %s: %w", n, e)
			}
			lines := bytes.Split(b, []byte{'\n'})
			if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
				lines = lines[:len(lines)-1]
			}
			for i, line := range lines {
				match := re.Match(line)
				if invert {
					match = !match
				}
				if !match {
					continue
				}
				found = true
				if files {
					fmt.Fprintln(c.OutOrStdout(), n)
					break
				}
				if numbers {
					fmt.Fprintf(c.OutOrStdout(), "%s:%d:%s\n", n, i+1, line)
				} else {
					fmt.Fprintf(c.OutOrStdout(), "%s:%s\n", n, line)
				}
			}
			clear(b)
		}
		if !found {
			return ErrNoMatch
		}
		return nil
	}}
	c.Flags().BoolVarP(&insensitive, "ignore-case", "i", false, "Ignore case")
	c.Flags().BoolVarP(&numbers, "line-number", "n", false, "Include line numbers")
	c.Flags().BoolVarP(&files, "files-with-matches", "l", false, "Print entry names only")
	c.Flags().BoolVarP(&invert, "invert-match", "v", false, "Select nonmatching lines")
	c.Flags().BoolVarP(&fixed, "fixed-strings", "F", false, "Match literal text")
	c.Flags().BoolVarP(&extended, "extended-regexp", "E", false, "Use Go extended regular expressions (default)")
	a.root.AddCommand(c)
}

// ErrNoMatch is grep's quiet unsuccessful-match result.
var ErrNoMatch = errors.New("no matching entries")

func (a *app) complete(c *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	s, e := a.open(false)
	if e != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer s.Close()
	names, e := s.List("")
	if e != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var result []string
	dirs := map[string]bool{}
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			result = append(result, n)
		}
		for d := path.Dir(n); d != "."; d = path.Dir(d) {
			if strings.HasPrefix(d, prefix) && !dirs[d] {
				dirs[d] = true
				result = append(result, d+"/")
			}
		}
	}
	sort.Strings(result)
	return result, cobra.ShellCompDirectiveNoFileComp
}
func (a *app) git() {
	c := &cobra.Command{Use: "git <command> [args]", DisableFlagParsing: true, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		conf, e := a.config()
		if e != nil {
			return e
		}
		if args[0] == "clone" {
			if len(args) != 2 {
				return fmt.Errorf("usage: git clone <url>")
			}
			return gitstore.Clone(conf.Dir, args[1], a.secret)
		}
		if args[0] == "init" {
			if len(args) != 1 {
				return fmt.Errorf("usage: git init")
			}
			if e = os.MkdirAll(conf.Dir, 0700); e != nil {
				return e
			}
			_, e = git.PlainInit(conf.Dir, false)
			if e != nil {
				return e
			}
			s, e := a.open(false)
			if e != nil {
				return e
			}
			defer s.Close()
			unlock, e := s.Lock()
			if e != nil {
				return e
			}
			defer unlock()
			if e = s.SetupGitIgnore(); e != nil {
				return e
			}
			p, e := s.GitFiles()
			if e != nil {
				return e
			}
			return gitstore.AutoCommit(conf.Dir, p, "Add current contents of password store.", c.ErrOrStderr())
		}
		r, e := gitstore.Open(conf.Dir)
		if e != nil {
			return e
		}
		s, e := a.open(false)
		if e != nil {
			return e
		}
		defer s.Close()
		unlock, e := s.Lock()
		if e != nil {
			return e
		}
		defer unlock()
		if args[0] == "pull" || args[0] == "push" || args[0] == "fetch" {
			return gitstore.Network(r, args, a.secret)
		}
		return gitstore.Local(r, conf.Dir, args, c.OutOrStdout(), s.Crypto.Decrypt)
	}}
	a.root.AddCommand(c)
}
