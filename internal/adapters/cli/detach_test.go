package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

type fakeRunner struct {
	started []domain.Job
	queued  bool // Start reports the job as queued behind a run
}

func (f *fakeRunner) Start(j domain.Job) (bool, error) {
	f.started = append(f.started, j)
	return f.queued, nil
}
func (f *fakeRunner) LogPath(t string) string  { return "runs/" + t + ".log" }
func (f *fakeRunner) Stop(string) error        { return nil }
func (f *fakeRunner) Dequeue(string, int, domain.Job) error { return nil }
func (f *fakeRunner) ClearQueue(string) (int, error)         { return 0, nil }
func (f *fakeRunner) Poll(string, int64) (app.RunStatus, error) {
	return app.RunStatus{}, nil
}

// newSvc returns a service with one target named "prod".
func newSvc(t *testing.T) *app.Service {
	t.Helper()
	svc, _ := newService(t)
	if err := Auth(svc, []string{"add", "prod", "postgres", "postgres://app:pw@db.example.com/shop"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestDetachStartsJobs(t *testing.T) {
	cases := []struct {
		args []string
		want domain.Job
	}{
		{[]string{"up", "--detach"}, domain.Job{Op: domain.OpUp}},
		{[]string{"up", "--detach", "-n", "2"}, domain.Job{Op: domain.OpUp, N: 2}},
		{[]string{"up", "--detach", "--to", "5"}, domain.Job{Op: domain.OpUpTo, Version: 5}},
		{[]string{"down", "--detach"}, domain.Job{Op: domain.OpDown}},
		{[]string{"down", "--detach", "--batch"}, domain.Job{Op: domain.OpBatch}},
		{[]string{"down", "--detach", "--to", "5"}, domain.Job{Op: domain.OpDownTo, Version: 5}},
		{[]string{"redo", "--detach", "7"}, domain.Job{Op: domain.OpRedo, Version: 7}},
	}
	for _, c := range cases {
		svc := newSvc(t)
		bg := &fakeRunner{}
		var out bytes.Buffer
		if err := MigrateWith(context.Background(), svc, bg, c.args, &out); err != nil {
			t.Fatalf("%v: %v\n%s", c.args, err, out.String())
		}
		if len(bg.started) != 1 {
			t.Fatalf("%v: started %v", c.args, bg.started)
		}
		got := bg.started[0]
		c.want.Target = got.Target
		if got != c.want || got.Target == "" {
			t.Errorf("%v: job = %+v, want %+v", c.args, got, c.want)
		}
		if !strings.Contains(out.String(), ".log") {
			t.Errorf("%v: output does not say where progress goes: %q", c.args, out.String())
		}
	}
}

func TestDetachSaysWhenTheJobWasQueued(t *testing.T) {
	bg := &fakeRunner{queued: true}
	var out bytes.Buffer
	if err := MigrateWith(context.Background(), newSvc(t), bg, []string{"up", "--detach"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "queued") {
		t.Errorf("output = %q", out.String())
	}
}

func TestDetachNeedsARunner(t *testing.T) {
	var out bytes.Buffer
	err := MigrateWith(context.Background(), newSvc(t), nil, []string{"up", "--detach"}, &out)
	if err == nil {
		t.Fatal("want an error without a runner")
	}
}
