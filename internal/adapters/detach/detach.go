// Package detach runs migrations in a worker process that survives the exit of
// the program that started it. A run leaves two files in <root>/.godwit/runs:
// <target>.pid (the worker's pid, then its job) and <target>.log (progress
// lines, ended by a "-- finished" line).
package detach

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// WorkerCommand is the first argument of a worker process.
const WorkerCommand = "__worker"

const finishedPrefix = "-- finished "

// Runner starts and observes background runs.
type Runner struct {
	root string
	exe  string
}

var _ app.BackgroundRunner = (*Runner)(nil)

// New returns a Runner that keeps its files under root and starts workers
// from the running executable.
func New(root string) (*Runner, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Runner{root: root, exe: exe}, nil
}

func (r *Runner) paths(target string) (pid, log string) {
	dir := filepath.Join(r.root, ".godwit", "runs")
	// Target names are user-chosen; keep the file name a single safe element.
	name := strings.Map(func(c rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, c) {
			return '_'
		}
		return c
	}, target)
	return filepath.Join(dir, name+".pid"), filepath.Join(dir, name+".log")
}

// Start launches a worker for j, which keeps running if this process exits.
// It refuses while an earlier run on the same target is still active.
func (r *Runner) Start(j domain.Job) error {
	pidFile, logFile := r.paths(j.Target)
	if st, _ := r.Poll(j.Target, 0); st.Active {
		return fmt.Errorf("%s already has a run in progress", j.Target)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		return err
	}
	cmd := exec.Command(r.exe, append([]string{WorkerCommand}, j.Args()...)...)
	cmd.Dir = r.root
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release() //nolint:errcheck // never waited on; it outlives us
	content := strconv.Itoa(pid) + "\n" + strings.Join(j.Args(), " ") + "\n"
	return os.WriteFile(pidFile, []byte(content), 0o644)
}

// Poll reads the log of target's latest run from byte offset from.
func (r *Runner) Poll(target string, from int64) (app.RunStatus, error) {
	pidFile, logFile := r.paths(target)
	meta, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return app.RunStatus{}, nil
	}
	if err != nil {
		return app.RunStatus{}, err
	}
	fields := strings.Fields(string(meta))
	if len(fields) != 5 {
		return app.RunStatus{}, fmt.Errorf("corrupt run file %s", pidFile)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return app.RunStatus{}, fmt.Errorf("corrupt run file %s", pidFile)
	}
	job, err := domain.ParseJob(fields[1:])
	if err != nil {
		return app.RunStatus{}, err
	}
	data, err := os.ReadFile(logFile)
	if err != nil && !os.IsNotExist(err) {
		return app.RunStatus{}, err
	}
	if from < 0 || from > int64(len(data)) {
		from = 0
	}
	st := app.RunStatus{Job: job, Next: from}
	data = data[from:]
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		st.Next += int64(i + 1)
		for line := range strings.SplitSeq(string(data[:i]), "\n") {
			if rest, ok := strings.CutPrefix(line, finishedPrefix); ok {
				st.Done = true
				st.Count, st.Err = parseFinished(rest)
				continue
			}
			st.Lines = append(st.Lines, line)
		}
	}
	if !st.Done {
		st.Active = alive(pid)
	}
	return st, nil
}

// parseFinished decodes "ok <n>" or "failed <n> <message>".
func parseFinished(s string) (count int, errMsg string) {
	word, rest, _ := strings.Cut(s, " ")
	num, msg, _ := strings.Cut(rest, " ")
	count, _ = strconv.Atoi(num)
	if word != "ok" {
		errMsg = msg
		if errMsg == "" {
			errMsg = "failed"
		}
	}
	return count, errMsg
}

// RunWorker is the body of a worker process: it performs the job described by
// args (the arguments after WorkerCommand), logging as it goes, and records
// the outcome last. run does the actual migrating.
func (r *Runner) RunWorker(ctx context.Context, args []string, run func(context.Context, domain.Job, app.Progress) (int, error)) error {
	job, err := domain.ParseJob(args)
	if err != nil {
		return err
	}
	_, logFile := r.paths(job.Target)
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prog := func(e domain.Event) { fmt.Fprintln(f, FormatEvent(e)) }
	n, runErr := run(ctx, job, prog)
	if runErr != nil {
		msg := strings.Join(strings.Fields(runErr.Error()), " ")
		fmt.Fprintf(f, "%sfailed %d %s\n", finishedPrefix, n, msg)
		return runErr
	}
	fmt.Fprintf(f, "%sok %d\n", finishedPrefix, n)
	return nil
}

// FormatEvent renders a progress event as one log line.
func FormatEvent(e domain.Event) string {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
	case domain.Started:
		return fmt.Sprintf("… %s %s", e.Direction, label)
	case domain.Done:
		return fmt.Sprintf("✓ %s %s", e.Direction, label)
	}
	return fmt.Sprintf("✗ %s %s: %s", e.Direction, label, strings.Join(strings.Fields(fmt.Sprint(e.Err)), " "))
}
