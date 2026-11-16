package detach

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// writeRecord writes a run record as Launch does.
func writeRecord(t *testing.T, r *Runner, j domain.Job, pid int) {
	t.Helper()
	pidFile, _ := r.paths(j.Ref())
	if err := ensureRunsDir(filepath.Dir(pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(pidFile, []byte(strconv.Itoa(pid)+"\n"+encodeJob(j)+"\n")); err != nil {
		t.Fatal(err)
	}
}

// Target names may hold spaces; the run and queue files keep them whole.
func TestRunFilesKeepNamesWithSpaces(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	for _, j := range []domain.Job{
		{Target: "my prod db", Op: domain.OpUp, N: 2},
		{Service: "users", Target: "shared dev db", Op: domain.OpRedo, Version: 7},
		{Target: "100% done", Op: domain.OpDown},
	} {
		writeRecord(t, r, j, os.Getpid())
		st, err := r.Poll(j.Ref(), 0)
		if err != nil || st.Job != j {
			t.Errorf("%q: job %+v, %v", j.Target, st.Job, err)
		}
		queued := []domain.Job{j, {Service: j.Service, Target: j.Target, Op: domain.OpBatch}}
		if err := r.writeQueue(j.Ref(), queued); err != nil {
			t.Fatal(err)
		}
		if got := r.readQueue(j.Ref()); len(got) != 2 || got[0] != j || got[1] != queued[1] {
			t.Errorf("%q: queue %+v", j.Target, got)
		}
	}
}

// Files written by earlier versions, with names as they are, still read.
func TestLegacyRunFilesStillRead(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	ref := domain.TargetRef{Target: "prod"}
	pidFile, _ := r.paths(ref)
	os.MkdirAll(filepath.Dir(pidFile), 0o755)
	os.WriteFile(pidFile, []byte("123\nprod up 2 0\n"), 0o644)
	if st, err := r.Poll(ref, 0); err != nil || st.Job != (domain.Job{Target: "prod", Op: domain.OpUp, N: 2}) || st.Pid != 123 {
		t.Errorf("legacy record: %+v, %v", st, err)
	}
	os.WriteFile(r.queueFile(ref), []byte("prod down 1 0\nprod redo 0 5\n"), 0o644)
	if q := r.readQueue(ref); len(q) != 2 || q[1].Version != 5 {
		t.Errorf("legacy queue: %+v", q)
	}
}

// A record that cannot be read names its file and says what to do.
func TestCorruptRunFileMessage(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	ref := domain.TargetRef{Target: "prod"}
	pidFile, _ := r.paths(ref)
	os.MkdirAll(filepath.Dir(pidFile), 0o755)
	for _, bad := range []string{"", "x\nprod up 0 0\n", "1\nprod sideways 0 0\n"} {
		os.WriteFile(pidFile, []byte(bad), 0o644)
		if _, err := r.Poll(ref, 0); err == nil {
			t.Errorf("%q read without error", bad)
		}
	}
}

func TestWriteAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.pid")
	for i := range 3 {
		if err := writeAtomic(file, []byte(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if data, _ := os.ReadFile(file); len(entries) != 1 || string(data) != "2" {
		t.Errorf("entries=%d data=%q", len(entries), data)
	}
}
