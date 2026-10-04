package domain

import (
	"slices"
	"testing"
)

func TestMergePlugins(t *testing.T) {
	global := []PluginSpec{{Name: "sqlite", Command: "/home/me/.godwit/plugins/sqlite"}, {Name: "duck", Command: "duck"}}
	project := []PluginSpec{{Name: "SQLite", Command: "./bin/sqlite"}, {Name: "ch", Command: "ch"}}

	got := MergePlugins(global, project)
	var names []string
	for _, p := range got {
		names = append(names, p.Name+"="+p.Command)
	}
	// A project entry replaces the global one of the same name (any case);
	// global entries come first.
	want := []string{"duck=duck", "SQLite=./bin/sqlite", "ch=ch"}
	if !slices.Equal(names, want) {
		t.Errorf("merged = %v; want %v", names, want)
	}

	if got := MergePlugins(nil, nil); len(got) != 0 {
		t.Errorf("empty merge = %v", got)
	}
	if got := MergePlugins(global, nil); len(got) != 2 {
		t.Errorf("global only = %v", got)
	}
	// The inputs are not modified.
	if len(global) != 2 || len(project) != 2 || global[0].Name != "sqlite" {
		t.Errorf("inputs changed: %v %v", global, project)
	}
}
