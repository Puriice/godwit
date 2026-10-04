package app

import (
	"errors"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestGlobalPlugins(t *testing.T) {
	project, global := newFakeStore(), newFakeStore()
	global.project = domain.Project{Plugins: []domain.PluginSpec{{Name: "existing", Command: "x"}}}
	svc, err := New(project, &fakeSource{}, &fakeFactory{db: newFakeDB()})
	if err != nil {
		t.Fatal(err)
	}

	// Without a global store (no home directory) global operations are refused.
	if svc.HasGlobal() {
		t.Error("HasGlobal before SetGlobalStore")
	}
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "a", Command: "a"}); err == nil {
		t.Error("global add without a store succeeded")
	}
	if err := svc.RemoveGlobalPlugin("a"); err == nil {
		t.Error("global remove without a store succeeded")
	}

	if err := svc.SetGlobalStore(global); err != nil {
		t.Fatal(err)
	}
	if !svc.HasGlobal() || len(svc.GlobalPlugins()) != 1 || svc.GlobalPlugins()[0].Name != "existing" {
		t.Fatalf("loaded global plugins = %+v", svc.GlobalPlugins())
	}

	// Global and project lists are independent, and each is saved to its own store.
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: " SQLite ", Command: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	if got := global.project.Plugins; len(got) != 2 || got[1].Name != "sqlite" {
		t.Errorf("global store = %+v", got)
	}
	if len(svc.Plugins()) != 0 || project.saves != 0 {
		t.Errorf("project touched: %+v saves=%d", svc.Plugins(), project.saves)
	}
	if err := svc.AddPlugin(domain.PluginSpec{Name: "sqlite", Command: "./local"}); err != nil {
		t.Fatalf("the same name in the project is allowed: %v", err)
	}
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "sqlite", Command: "other"}); err == nil {
		t.Error("duplicate global name accepted")
	}
	for _, bad := range []domain.PluginSpec{{Command: "x"}, {Name: "n"}} {
		if err := svc.AddGlobalPlugin(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}

	if err := svc.RemoveGlobalPlugin("SQLite"); err != nil || len(global.project.Plugins) != 1 || len(svc.Plugins()) != 1 {
		t.Errorf("remove global: %v global=%+v project=%+v", err, global.project.Plugins, svc.Plugins())
	}
	if err := svc.RemoveGlobalPlugin("nope"); err == nil {
		t.Error("removing an unknown global plugin succeeded")
	}
}

type failingLoad struct{ *fakeStore }

func (failingLoad) Load() (domain.Project, error) { return domain.Project{}, errors.New("corrupt") }

func TestSetGlobalStoreSurfacesLoadErrors(t *testing.T) {
	svc, _ := New(newFakeStore(), &fakeSource{}, &fakeFactory{db: newFakeDB()})
	if err := svc.SetGlobalStore(failingLoad{newFakeStore()}); err == nil || svc.HasGlobal() {
		t.Errorf("err=%v hasGlobal=%v", err, svc.HasGlobal())
	}
}
