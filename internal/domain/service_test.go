package domain

import (
	"strings"
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	for _, ok := range []string{"users", "a1_b", "x", strings.Repeat("a", MaxServiceName)} {
		if err := ValidateServiceName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Users", "1a", "a-b", "a b", "_a", "a/b", strings.Repeat("a", MaxServiceName+1)} {
		if ValidateServiceName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestStateTable(t *testing.T) {
	if StateTable("") != "godwit_migration" || StateTable("users") != "godwit_migration_users" {
		t.Errorf("StateTable = %q, %q", StateTable(""), StateTable("users"))
	}
}

func TestTargetRef(t *testing.T) {
	for _, r := range []TargetRef{{Target: "dev"}, {Service: "users", Target: "dev"}} {
		if got := ParseTargetRef(r.String()); got != r {
			t.Errorf("round trip %+v -> %q -> %+v", r, r.String(), got)
		}
	}
	if (TargetRef{Service: "users", Target: "dev"}).String() != "users/dev" {
		t.Error("String")
	}
}

func TestJobArgs(t *testing.T) {
	plain := Job{Target: "dev", Op: OpUpTo, N: 2, Version: 7}
	if a := plain.Args(); len(a) != 4 {
		t.Errorf("a job without a service keeps 4 arguments: %v", a)
	}
	svc := Job{Service: "users", Target: "dev", Op: OpRedo, Version: 9}
	for _, j := range []Job{plain, svc} {
		got, err := ParseJob(j.Args())
		if err != nil || got != j {
			t.Errorf("round trip %+v -> %+v, %v", j, got, err)
		}
	}
	if j, err := ParseJob([]string{"dev", "up", "0", "0"}); err != nil || j.Service != "" || j.Ref() != (TargetRef{Target: "dev"}) {
		t.Errorf("legacy job = %+v, %v", j, err)
	}
	for _, bad := range [][]string{{"dev", "up", "0"}, {"dev", "up", "0", "0", "users", "x"}, {"dev", "up", "0", "0", "Bad-Name"}} {
		if _, err := ParseJob(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestMergeWorkspacePlugins(t *testing.T) {
	base := []PluginSpec{{Name: "duck", Command: "global-duck"}}
	merged, warns := MergeWorkspacePlugins(base, []ServicePlugins{
		{Service: "users", Plugins: []PluginSpec{{Name: "jsonfile", Command: "a"}, {Name: "duck", Command: "svc-duck"}}},
		{Service: "orders", Plugins: []PluginSpec{{Name: "JSONFile", Command: "b"}, {Name: "duck", Command: "svc-duck"}}},
	})
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	byName := map[string]string{}
	for _, p := range merged {
		byName[strings.ToLower(p.Name)] = p.Command
	}
	if byName["jsonfile"] != "a" || byName["duck"] != "svc-duck" {
		t.Errorf("merged = %+v", merged)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "users and orders") {
		t.Errorf("warnings = %v (only the conflicting jsonfile should warn)", warns)
	}
}
