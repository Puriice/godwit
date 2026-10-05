package detach

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests run the real godwit and the example jsonfile plugin, so a detached
// worker, its queue and a cancellation are exercised through a driver plugin.
// They build two binaries, so they are skipped with -short.

// buildGo builds the Go package at pkg (relative to this directory) into dir
// and returns the executable's path.
func buildGo(t *testing.T, dir, pkg string) string {
	t.Helper()
	out := filepath.Join(dir, filepath.Base(pkg))
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
	return out
}

// pluginProject is a project with n migrations and one "local" target on the
// jsonfile plugin, whose migrations each take delay to apply.
type pluginProject struct {
	t      *testing.T
	dir    string
	godwit string
}

func newPluginProject(t *testing.T, n int, delay time.Duration) *pluginProject {
	t.Helper()
	if testing.Short() {
		t.Skip("builds godwit and a plugin")
	}
	bin := t.TempDir()
	p := &pluginProject{
		t:      t,
		dir:    t.TempDir(),
		godwit: buildGo(t, bin, "../../../cmd/godwit"),
	}
	plugin := buildGo(t, bin, "../../../examples/driver-jsonfile")
	// The delay is inherited by the worker and, through it, by the plugin.
	t.Setenv("GODWIT_JSONFILE_DELAY", delay.String())

	if err := os.MkdirAll(filepath.Join(p.dir, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		sql := fmt.Sprintf("-- +godwit Up\nCREATE TABLE t%d (id INTEGER);\n\n-- +godwit Down\nDROP TABLE t%d;\n", i, i)
		name := filepath.Join(p.dir, "migrations", fmt.Sprintf("%d_m%d.sql", i, i))
		if err := os.WriteFile(name, []byte(sql), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p.run("plugin", "add", plugin)
	p.run("auth", "add", "local", "jsonfile", "jsonfile://state.json")
	return p
}

// run executes godwit in the project and returns its output.
func (p *pluginProject) run(args ...string) string {
	p.t.Helper()
	cmd := exec.Command(p.godwit, args...)
	cmd.Dir = p.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("godwit %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (p *pluginProject) runsFile(name string) string {
	return filepath.Join(p.dir, ".godwit", "runs", name)
}

// waitLog waits until the target's log contains want, and returns the log.
func (p *pluginProject) waitLog(want string) string {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, _ := os.ReadFile(p.runsFile("local.log"))
		if strings.Contains(string(raw), want) {
			return string(raw)
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("log never contained %q:\n%s", want, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *pluginProject) exists(name string) bool {
	_, err := os.Stat(p.runsFile(name))
	return err == nil
}

func TestPluginRunHandsOverToTheQueuedJob(t *testing.T) {
	p := newPluginProject(t, 3, 1500*time.Millisecond)

	if out := p.run("migrate", "up", "--detach", "-n", "1"); !strings.Contains(out, "started in the background") {
		t.Fatalf("first job: %s", out)
	}
	if out := p.run("migrate", "up", "--detach"); !strings.Contains(out, "queued") {
		t.Fatalf("second job was not queued behind the first: %s", out)
	}
	if !p.exists("local.queue") {
		t.Error("the queue file is missing while a job waits")
	}

	// The queued job applies the two migrations the first left, in a new worker.
	log := p.waitLog("-- finished ok 2")
	for _, want := range []string{"✓ up 2_m2", "✓ up 3_m3"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if p.exists("local.queue") {
		t.Error("the queue file is left behind after its last job started")
	}
	if status := p.run("migrate", "status"); strings.Count(status, "applied") != 3 {
		t.Errorf("status:\n%s", status)
	}
}

func TestPluginRunCancelDropsTheQueue(t *testing.T) {
	p := newPluginProject(t, 3, 2*time.Second)

	p.run("migrate", "up", "--detach")
	if out := p.run("migrate", "up", "--detach", "-n", "1"); !strings.Contains(out, "queued") {
		t.Fatalf("second job was not queued: %s", out)
	}
	p.waitLog("… up 1_m1") // the plugin is in the middle of a migration

	r := &Runner{root: p.dir}
	if err := r.Stop("local"); err != nil {
		t.Fatal(err)
	}
	log := p.waitLog("-- finished cancelled")
	if !strings.Contains(log, "1 queued job(s) dropped") {
		t.Errorf("the dropped queue is not logged:\n%s", log)
	}
	if p.exists("local.queue") {
		t.Error("a queue survived the cancellation")
	}

	// The migration that was cut off is left dirty; the rest never started.
	status := p.run("migrate", "status")
	if strings.Count(status, "dirty") != 1 || strings.Count(status, "pending") != 2 {
		t.Errorf("status:\n%s", status)
	}
}
