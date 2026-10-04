package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTargetFormFitsShortTerminalAtTop(t *testing.T) {
	for _, h := range []int{8, 12, 40} {
		m := testModel(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: h})
		m.openTargetForm(nil)
		lines := strings.Split(m.View(), "\n")
		if len(lines) > h {
			t.Errorf("height %d: view has %d lines", h, len(lines))
		}
		view := strings.Join(lines, "\n")
		if !strings.Contains(view, "Name") {
			t.Errorf("height %d: first field (Name) not visible:\n%s", h, view)
		}
	}
}

func TestTargetInput(t *testing.T) {
	drivers := []string{"mysql", "postgres"}

	in := newTargetInput(drivers, nil)
	if in.t.Driver != "mysql" || in.t.Host != "localhost" || !in.persist {
		t.Fatalf("defaults: %+v", in)
	}
	in.t.Name, in.port = "  prod ", "5432"
	got := in.target()
	if got.Name != "prod" || got.Port != 5432 {
		t.Fatalf("target: %+v", got)
	}

	// Editing prefills every field, including the port.
	ed := newTargetInput(drivers, &got)
	if ed.port != "5432" || ed.t.Name != "prod" {
		t.Errorf("prefill: %+v", ed)
	}
	ed.t.Host = "db.example.com"
	if h := ed.target().Host; h != "db.example.com" {
		t.Errorf("host = %q", h)
	}
}
