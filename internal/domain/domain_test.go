package domain

import "testing"

func TestStatementsForFiltersByDriver(t *testing.T) {
	m := &Migration{Up: []Statement{
		{Driver: DriverAll, SQL: "a;"},
		{Driver: "postgres", SQL: "b;"},
		{Driver: "mysql", SQL: "c;"},
	}}
	if got := m.UpSQL("MySQL"); len(got) != 2 || got[0] != "a;" || got[1] != "c;" {
		t.Errorf("mysql = %v", got)
	}
	if got := m.UpSQL("postgres"); len(got) != 2 || got[1] != "b;" {
		t.Errorf("postgres = %v", got)
	}
	if got := m.DownSQL("postgres"); len(got) != 0 {
		t.Errorf("down = %v", got)
	}
}

func TestProjectTarget(t *testing.T) {
	p := Project{Targets: []Target{{Name: "a"}, {Name: "b"}}}
	if _, ok := p.Target("b"); !ok {
		t.Error("b not found")
	}
	if _, ok := p.Target("z"); ok {
		t.Error("z found")
	}
}
