package tui

import (
	"testing"

	"github.com/puriice/godwit/internal/config"
)

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
