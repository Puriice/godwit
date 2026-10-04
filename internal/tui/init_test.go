package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/config"
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

func TestTargetInputApply(t *testing.T) {
	p, _ := config.Load(t.TempDir())

	in := newTargetInput(nil)
	if in.t.Driver == "" || in.t.Host != "localhost" || !in.persist {
		t.Fatalf("defaults: %+v", in)
	}
	in.t.Name, in.port = "  prod ", "5432"
	got := in.apply(p, nil)
	if got.Name != "prod" || got.Port != 5432 || len(p.Targets) != 1 {
		t.Fatalf("add: %+v targets=%+v", got, p.Targets)
	}

	// Editing replaces in place, keeps the name, and prefills the port.
	existing := p.Targets[0]
	ed := newTargetInput(&existing)
	if ed.port != "5432" {
		t.Errorf("port prefill = %q", ed.port)
	}
	ed.t.Host = "db.example.com"
	ed.apply(p, &existing)
	if len(p.Targets) != 1 || p.Targets[0].Host != "db.example.com" {
		t.Errorf("edit: %+v", p.Targets)
	}
}
