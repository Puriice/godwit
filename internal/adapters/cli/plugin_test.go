package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestPluginCommands(t *testing.T) {
	svc, _ := newService(t)
	var out bytes.Buffer
	probe := func(spec domain.PluginSpec) (domain.DriverInfo, error) {
		switch spec.Command {
		case "broken":
			return domain.DriverInfo{}, errors.New("cannot find broken")
		case "wrongname":
			return domain.DriverInfo{Name: "other"}, nil
		}
		return domain.DriverInfo{Name: spec.Name}, nil
	}

	if err := Plugin(svc, probe, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "No plugins") {
		t.Fatalf("empty list: %q %v", out.String(), err)
	}

	for _, args := range [][]string{{"add", "x", "broken"}, {"add", "sqlite", "wrongname"}} {
		if err := Plugin(svc, probe, args, &out); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	if len(svc.Plugins()) != 0 {
		t.Fatal("a plugin that failed its probe was saved")
	}

	out.Reset()
	if err := Plugin(svc, probe, []string{"add", "sqlite", "./bin/godwit-driver-sqlite", "--flag"}, &out); err != nil {
		t.Fatal(err)
	}
	got := svc.Plugins()
	if len(got) != 1 || got[0].Command != "./bin/godwit-driver-sqlite" || len(got[0].Args) != 1 {
		t.Fatalf("plugins = %+v", got)
	}

	out.Reset()
	if err := Plugin(svc, probe, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "sqlite") || !strings.Contains(out.String(), "--flag") {
		t.Errorf("list = %q, %v", out.String(), err)
	}
	if err := Plugin(svc, probe, []string{"remove", "sqlite"}, &out); err != nil || len(svc.Plugins()) != 0 {
		t.Errorf("remove: %v", err)
	}
	for _, args := range [][]string{nil, {"add", "only-name"}, {"remove"}, {"bogus"}} {
		if err := Plugin(svc, probe, args, &out); err == nil || !strings.Contains(err.Error(), "usage: godwit plugin") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}
