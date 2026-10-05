package domain

import "testing"

func TestStepsForDropsEmptyBlocks(t *testing.T) {
	m := &Migration{Up: []Statement{
		{Driver: "mysql", SQL: "a;", Group: 1},
		{Driver: DriverAll, SQL: "cond;", Group: 1, Condition: true},
		{Driver: DriverAll, SQL: "b;"},
	}}
	got := m.UpSteps("postgres")
	if len(got) != 1 || got[0].Repeat || got[0].SQL[0] != "b;" {
		t.Errorf("steps = %+v", got)
	}
	if !m.UpHasRepeat() || m.DownHasRepeat() {
		t.Error("HasRepeat wrong")
	}
}
