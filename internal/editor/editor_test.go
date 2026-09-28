package editor

import (
	"os"
	"reflect"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func TestSaveAndCancel(t *testing.T) {
	for _, tc := range []struct {
		key   tea.KeyPressMsg
		saved bool
	}{{tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, true}, {tea.KeyPressMsg{Code: tea.KeyEscape}, false}, {tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, false}} {
		a := textarea.New()
		a.SetValue("secret\nmetadata\n\n")
		m := model{area: a}
		got, _ := m.Update(tc.key)
		result := got.(model)
		if result.saved != tc.saved {
			t.Fatal("incorrect save state")
		}
		if result.area.Value() != "secret\nmetadata\n\n" {
			t.Fatal("editor altered content")
		}
	}
}

func TestModelConfiguration(t *testing.T) {
	for _, theme := range []string{"", "dark", "light"} {
		m, err := newModel([]byte("日本語\nmetadata\n\n"), theme)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.area.Styles(), textarea.DefaultStyles(theme != "light")) {
			t.Fatal("wrong theme")
		}
		if m.area.KeyMap.Paste.Enabled() {
			t.Fatal("external clipboard binding enabled")
		}
		if m.area.CharLimit != 0 || !m.area.ShowLineNumbers || !m.area.VirtualCursor() {
			t.Fatal("changed editor configuration")
		}
		if !m.View().AltScreen {
			t.Fatal("not using alternate screen")
		}
		got, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
		_ = got.(model).View()
		if got.(model).area.Value() != "日本語\nmetadata\n\n" {
			t.Fatal("resize changed content")
		}
	}
	for _, data := range [][]byte{{0}, {0xff}} {
		if _, err := newModel(data, "dark"); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	t.Setenv("PASSGAGE_EDITOR_THEME", "auto")
	if _, err := Edit([]byte("secret")); err == nil {
		t.Fatal("invalid theme accepted")
	}
}

func TestPasteMessages(t *testing.T) {
	for _, msg := range []tea.Msg{pasted("日本語\nmetadata"), tea.PasteMsg{Content: "日本語\nmetadata"}} {
		m, err := newModel(nil, "dark")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := m.Update(msg)
		if got.(model).area.Value() != "日本語\nmetadata" {
			t.Fatalf("paste: %q", got.(model).area.Value())
		}
	}
}

func TestExternalThemeHelper(t *testing.T) {
	if os.Getenv("PASSGAGE_EXTERNAL_THEME_HELPER") == "1" {
		os.Exit(0)
	}
}

func TestExternalIgnoresTheme(t *testing.T) {
	t.Setenv("PASSGAGE_EDITOR_THEME", "invalid")
	t.Setenv("PASSGAGE_EXTERNAL_THEME_HELPER", "1")
	b, err := External([]byte("synthetic\n\n"), os.Args[0], []string{"-test.run=^TestExternalThemeHelper$", "--"})
	if err != nil || string(b) != "synthetic\n\n" {
		t.Fatalf("external editor: %q %v", b, err)
	}
}
