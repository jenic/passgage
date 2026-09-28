// Package editor implements in-memory editing and explicit external editing.
package editor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	clipboard "golang.design/x/clipboard"
)

// ErrCanceled indicates the user chose not to save.
var ErrCanceled = errors.New("editing canceled")

type model struct {
	area  textarea.Model
	saved bool
	err   error
}
type pasted string

func (m model) Init() tea.Cmd { return textarea.Blink }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.area.SetWidth(max(1, v.Width-2))
		m.area.SetHeight(max(1, v.Height-3))
	case pasted:
		m.area.InsertString(string(v))
		return m, nil
	case tea.KeyPressMsg:
		switch v.String() {
		case "ctrl+s":
			m.saved = true
			return m, tea.Quit
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "ctrl+v":
			return m, func() tea.Msg {
				if clipboard.Init() != nil {
					return pasted("")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				b, _ := clipboard.Read(ctx, clipboard.FmtText)
				return pasted(b)
			}
		}
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}
func (m model) View() tea.View {
	v := tea.NewView(m.area.View() + "\nCtrl+S save · Esc/Ctrl+C cancel · Ctrl+V paste\n")
	v.AltScreen = true
	return v
}

// Edit opens a terminal editor without a plaintext temporary file.
func Edit(b []byte) ([]byte, error) {
	m, err := newModel(b, os.Getenv("PASSGAGE_EDITOR_THEME"))
	if err != nil {
		return nil, err
	}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return nil, err
	}
	m = result.(model)
	if !m.saved {
		return nil, ErrCanceled
	}
	return []byte(m.area.Value()), nil
}

func newModel(b []byte, theme string) (model, error) {
	if theme != "" && theme != "dark" && theme != "light" {
		return model{}, fmt.Errorf("PASSGAGE_EDITOR_THEME must be dark or light")
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return model{}, fmt.Errorf("terminal editing requires UTF-8 text without NUL bytes; use insert --multiline for binary data")
	}
	a := textarea.New()
	a.SetStyles(textarea.DefaultStyles(theme != "light"))
	a.CharLimit = 0
	a.SetWidth(80)
	a.SetHeight(20)
	a.ShowLineNumbers = true
	a.KeyMap.Paste.SetEnabled(false)
	a.SetValue(string(b))
	a.Focus()
	return model{area: a}, nil
}

// External invokes only the executable and arguments explicitly supplied by the user.
func External(b []byte, executable string, args []string) ([]byte, error) {
	if executable == "" {
		return nil, fmt.Errorf("an external editor executable is required")
	}
	dir, err := os.MkdirTemp("", "passgage-edit-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err = secure(dir); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "entry.txt")
	if err = os.WriteFile(p, b, 0600); err != nil {
		return nil, err
	}
	if err = secure(p); err != nil {
		return nil, err
	}
	argv := append(append([]string{}, args...), p)
	cmd := exec.Command(executable, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
