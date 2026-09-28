package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/muesli/termenv"
)

type treeNode struct {
	name     string
	dir      bool
	children map[string]*treeNode
}

func displayName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		} else {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		}
	}
	return b.String()
}

// listingColor deliberately ignores forced-color settings: pipes stay plain.
func listingColor(out *termenv.Output, getenv func(string) string, enable func(*termenv.Output) (func() error, error)) (bool, func() error) {
	noop := func() error { return nil }
	if getenv("NO_COLOR") != "" || getenv("CLICOLOR") == "0" || getenv("TERM") == "dumb" || out.ColorProfile() == termenv.Ascii {
		return false, noop
	}
	restore, err := enable(out)
	if err != nil {
		return false, noop
	}
	return true, restore
}

func writeListing(w io.Writer, prefix string, names []string) (err error) {
	out := termenv.NewOutput(w, termenv.WithProfile(termenv.ANSI))
	color, restore := listingColor(out, os.Getenv, func(o *termenv.Output) (func() error, error) {
		return termenv.EnableVirtualTerminalProcessing(o)
	})
	defer func() {
		if e := restore(); err == nil {
			err = e
		}
	}()
	return renderTree(w, prefix, names, color)
}

func renderTree(w io.Writer, prefix string, names []string, color bool) error {
	prefix = strings.TrimSuffix(prefix, "/")
	heading := prefix
	if heading == "" {
		heading = "Passgage"
	}
	root := &treeNode{dir: true, children: make(map[string]*treeNode)}
	for _, name := range names {
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix+"/")
		}
		parts := strings.Split(name, "/")
		node := root
		for i, part := range parts {
			dir := i < len(parts)-1
			// A directory and an entry may have the same logical name.
			key := fmt.Sprintf("%t:%s", dir, part)
			child := node.children[key]
			if child == nil {
				child = &treeNode{name: part, dir: dir, children: make(map[string]*treeNode)}
				node.children[key] = child
			}
			node = child
		}
	}
	var b strings.Builder
	b.WriteString(displayName(heading) + "\n")
	var walk func(*treeNode, string)
	walk = func(node *treeNode, indent string) {
		children := make([]*treeNode, 0, len(node.children))
		for _, child := range node.children {
			children = append(children, child)
		}
		sort.Slice(children, func(i, j int) bool {
			if children[i].name == children[j].name {
				return children[i].dir && !children[j].dir
			}
			return children[i].name < children[j].name
		})
		for i, child := range children {
			branch, next := "├── ", "│   "
			if i == len(children)-1 {
				branch, next = "└── ", "    "
			}
			label := displayName(child.name)
			if color && child.dir {
				label = "\x1b[1;34m" + label + "\x1b[0m"
			}
			b.WriteString(indent + branch + label + "\n")
			walk(child, indent+next)
		}
	}
	walk(root, "")
	n, err := io.WriteString(w, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
