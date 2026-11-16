// Package detach runs migrations in a worker process that survives the exit of
// the program that started it. A run leaves two files in <root>/.godwit/runs
// (in runs/<service> for a service of a workspace): <target>.pid (the
// worker's pid, then its job) and <target>.log (progress lines, ended by a
// "-- finished" line).
package detach

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// WorkerCommand is the first argument of a worker process.
const WorkerCommand = "__worker"

// LaunchCommand is the first argument of the process that starts a worker.
const LaunchCommand = "__launch"

const (
	finishedPrefix = "-- finished "
	queuedPrefix   = "· queued "
	donePrefix     = "✓ "
)

// lineVersion reads the version out of "<dir> <version>_<name>".
func lineVersion(s string) (int64, bool) {
	_, label, ok := strings.Cut(s, " ")
	if !ok {
		return 0, false
	}
	num, _, _ := strings.Cut(label, "_")
	v, err := strconv.ParseInt(num, 10, 64)
	return v, err == nil
}

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

// paths returns a run's pid and log files: runs/<target>.* for a plain
// project, runs/<service>/<target>.* for a service of a workspace, so that
// services sharing a global target keep separate runs and queues on it.
func (r *Runner) paths(ref domain.TargetRef) (pid, log string) {
	dir := filepath.Join(r.root, ".godwit", "runs")
	if ref.Service != "" {
		dir = filepath.Join(dir, safeName(ref.Service))
	}
	name := safeName(ref.Target)
	return filepath.Join(dir, name+".pid"), filepath.Join(dir, name+".log")
}

// safeName keeps a user-chosen name a single safe file name element.
func safeName(s string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, c) {
			return '_'
		}
		return c
	}, s)
}

// LogPath is the file a target's run logs its progress to.
func (r *Runner) LogPath(ref domain.TargetRef) string {
	_, log := r.paths(ref)
	return log
}

// chainGrace is how long a finished worker waits before handing over to the
// next queued job, so that anyone following the log sees the run end first:
// the next run starts a fresh log.
var chainGrace = time.Second

// handoverWait bounds how long Start waits for a worker that is just exiting.
const handoverWait = 5 * time.Second

// nextLine marks a run that hands over to a queued job.
const nextLine = "-- next"

// ensureRunsDir creates dir, which holds one machine's pid, log, queue and
// lock files, and keeps it all out of version control with a .gitignore of its
// own, so it also works in projects whose .godwit/.gitignore predates it.
func ensureRunsDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		return os.WriteFile(ignore, []byte("*\n!.gitignore\n"), 0o644)
	}
	return nil
}

func (r *Runner) queueFile(ref domain.TargetRef) string {
	pid, _ := r.paths(ref)
	return strings.TrimSuffix(pid, ".pid") + ".queue"
}

// lock serialises changes to a target's queue and run files between this
// program and its worker. The returned function releases it.
func (r *Runner) lock(ref domain.TargetRef) (func(), error) {
	pid, _ := r.paths(ref)
	file := strings.TrimSuffix(pid, ".pid") + ".lock"
	if err := ensureRunsDir(filepath.Dir(file)); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(handoverWait)
	for {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { os.Remove(file) }, nil //nolint:errcheck // best effort
		}
		if time.Now().After(deadline) {
			// Held for far longer than any holder needs: left by a crash.
			if err := os.Remove(file); err != nil {
				return nil, err
			}
			deadline = time.Now().Add(handoverWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readQueue lists the jobs waiting on target, oldest first.
func (r *Runner) readQueue(ref domain.TargetRef) []domain.Job {
	data, err := os.ReadFile(r.queueFile(ref))
	if err != nil {
		return nil
	}
	var jobs []domain.Job
	for line := range strings.SplitSeq(string(data), "\n") {
		if j, err := decodeJob(line); err == nil {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

func (r *Runner) writeQueue(ref domain.TargetRef, jobs []domain.Job) error {
	file := r.queueFile(ref)
	if len(jobs) == 0 {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var b strings.Builder
	for _, j := range jobs {
		b.WriteString(encodeJob(j))
		b.WriteByte('\n')
	}
	return writeAtomic(file, []byte(b.String()))
}

// encodeJob writes a job as one line of space-separated words for the run and
// queue files. Each word is escaped, so a target name with spaces stays one
// word.
func encodeJob(j domain.Job) string {
	args := j.Args()
	for i, a := range args {
		args[i] = url.PathEscape(a)
	}
	return strings.Join(args, " ")
}

// decodeJob reads a line written by encodeJob. Files written before names
// were escaped hold them as they are; a word that is not valid escaping is
// taken literally.
func decodeJob(line string) (domain.Job, error) {
	words := strings.Fields(line)
	for i, w := range words {
		if u, err := url.PathUnescape(w); err == nil {
			words[i] = u
		}
	}
	return domain.ParseJob(words)
}

// writeAtomic replaces file with data in one step, so a reader polling it
// (the TUI, another godwit) never sees it empty or half written.
func writeAtomic(file string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), file)
	}
	if werr != nil {
		os.Remove(tmp.Name()) //nolint:errcheck // best effort
	}
	return werr
}

// Dequeue removes the job at position index of target's queue if it is still
// j. The position matters: a queue may hold the same job more than once.
func (r *Runner) Dequeue(ref domain.TargetRef, index int, j domain.Job) error {
	unlock, err := r.lock(ref)
	if err != nil {
		return err
	}
	defer unlock()
	queue := r.readQueue(ref)
	if index < 0 || index >= len(queue) || queue[index] != j {
		return fmt.Errorf("the queue of %s has changed (that job may have started); check it and try again", ref)
	}
	return r.writeQueue(ref, slices.Delete(queue, index, index+1))
}

// ClearQueue removes every job queued on target and returns how many.
func (r *Runner) ClearQueue(ref domain.TargetRef) (int, error) {
	unlock, err := r.lock(ref)
	if err != nil {
		return 0, err
	}
	defer unlock()
	n := len(r.readQueue(ref))
	return n, r.writeQueue(ref, nil)
}

// busy reports whether target has a live worker, even one that has already
// logged its end and is about to exit or to hand over.
func (r *Runner) busy(ref domain.TargetRef) bool {
	st, _ := r.Poll(ref, 0)
	return st.Pid != 0 && alive(st.Pid)
}

// Start launches a worker for j, which keeps running if this process exits.
// While the target has a run, j is queued behind it instead.
func (r *Runner) Start(j domain.Job) (bool, error) {
	unlock, err := r.lock(j.Ref())
	if err != nil {
		return false, err
	}
	defer unlock()
	if r.busy(j.Ref()) {
		if st, _ := r.Poll(j.Ref(), 0); st.Done && !st.More {
			// The worker has decided there is nothing more to do and is on its
			// way out; wait for it rather than queue behind nobody.
			deadline := time.Now().Add(handoverWait)
			for r.busy(j.Ref()) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	if r.busy(j.Ref()) {
		queue := append(r.readQueue(j.Ref()), j)
		return true, r.writeQueue(j.Ref(), queue)
	}
	r.writeQueue(j.Ref(), nil) //nolint:errcheck // jobs queued behind a worker that died
	return false, r.spawn(j, false)
}

// spawn starts the worker for j with a fresh log. chained is set by the
// previous worker, which is still alive: its record stays until the new one
// replaces it, so the target never looks idle in between.
func (r *Runner) spawn(j domain.Job, chained bool) error {
	pidFile, logFile := r.paths(j.Ref())
	if err := ensureRunsDir(filepath.Dir(pidFile)); err != nil {
		return err
	}
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		return err
	}
	if !chained {
		os.Remove(pidFile) //nolint:errcheck // a stale record of the previous run
	}
	os.Remove(r.cancelFile(j.Ref())) //nolint:errcheck // and its cancel request
	// The worker is started by a short-lived launcher, not by this process.
	// Once the launcher exits the worker has no live parent, so killing this
	// process's tree (a closing terminal, taskkill /T) cannot reach it.
	cmd := exec.Command(r.exe, append([]string{LaunchCommand}, j.Args()...)...)
	cmd.Dir = r.root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting worker: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Launch is the body of the launcher process: it starts a detached worker for
// the job in args and records its pid, then returns so the launcher can exit.
func (r *Runner) Launch(args []string) error {
	j, err := domain.ParseJob(args)
	if err != nil {
		return err
	}
	pidFile, _ := r.paths(j.Ref())
	cmd := exec.Command(r.exe, append([]string{WorkerCommand}, j.Args()...)...)
	cmd.Dir = r.root
	if err := startDetached(cmd); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release() //nolint:errcheck // never waited on; it outlives us
	content := strconv.Itoa(pid) + "\n" + encodeJob(j) + "\n"
	return writeAtomic(pidFile, []byte(content))
}

// Poll reads the log of target's latest run from byte offset from.
func (r *Runner) Poll(ref domain.TargetRef, from int64) (app.RunStatus, error) {
	pidFile, logFile := r.paths(ref)
	meta, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return app.RunStatus{}, nil
	}
	if err != nil {
		return app.RunStatus{}, err
	}
	// A pid line, then the job (see encodeJob).
	pidLine, jobLine, _ := strings.Cut(string(meta), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(pidLine))
	if err != nil {
		return app.RunStatus{}, fmt.Errorf("corrupt run file %s: delete it if no run is going on that target", pidFile)
	}
	job, err := decodeJob(jobLine)
	if err != nil {
		return app.RunStatus{}, fmt.Errorf("corrupt run file %s (%v): delete it if no run is going on that target", pidFile, err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil && !os.IsNotExist(err) {
		return app.RunStatus{}, err
	}
	if from < 0 || from > int64(len(data)) {
		from = 0
	}
	st := app.RunStatus{Job: job, Pid: pid, Next: from, Pending: r.readQueue(ref)}
	data = data[from:]
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		st.Next += int64(i + 1)
		for line := range strings.SplitSeq(string(data[:i]), "\n") {
			if line == nextLine {
				st.More = true
				continue
			}
			if rest, ok := strings.CutPrefix(line, finishedPrefix); ok {
				st.Done = true
				st.Count, st.Err, st.Cancelled = parseFinished(rest)
				continue
			}
			if rest, ok := strings.CutPrefix(line, queuedPrefix); ok {
				if v, ok := lineVersion(rest); ok {
					st.Queued = append(st.Queued, v)
				}
				continue
			}
			if rest, ok := strings.CutPrefix(line, donePrefix); ok {
				if v, ok := lineVersion(rest); ok {
					st.Finished = append(st.Finished, v)
				}
			}
			st.Lines = append(st.Lines, line)
		}
	}
	if !st.Done {
		st.Active = alive(pid)
		if _, err := os.Stat(r.cancelFile(ref)); err == nil {
			st.Cancelling = st.Active
		}
	}
	return st, nil
}

// parseFinished decodes "ok <n>", "failed <n> <message>" or
// "cancelled <n> <message>".
func parseFinished(s string) (count int, errMsg string, cancelled bool) {
	word, rest, _ := strings.Cut(s, " ")
	num, msg, _ := strings.Cut(rest, " ")
	count, _ = strconv.Atoi(num)
	if word != "ok" {
		errMsg = msg
		if errMsg == "" {
			errMsg = word
		}
	}
	return count, errMsg, word == "cancelled"
}

func (r *Runner) cancelFile(ref domain.TargetRef) string {
	pid, _ := r.paths(ref)
	return strings.TrimSuffix(pid, ".pid") + ".cancel"
}

// Stop asks target's worker to cancel its run. The worker cancels the context
// of the migration it is executing and records the run as cancelled, so a
// statement in flight is aborted and no further migration starts.
func (r *Runner) Stop(ref domain.TargetRef) error {
	if st, _ := r.Poll(ref, 0); !st.Active {
		return fmt.Errorf("%s has no run in progress", ref)
	}
	return os.WriteFile(r.cancelFile(ref), nil, 0o644)
}

// watchCancel cancels ctx when target's cancel file appears.
func (r *Runner) watchCancel(ctx context.Context, ref domain.TargetRef, cancel context.CancelFunc) {
	file := r.cancelFile(ref)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := os.Stat(file); err == nil {
				cancel()
				return
			}
		}
	}
}

// RunWorker is the body of a worker process: it performs the job described by
// args (the arguments after WorkerCommand), logging as it goes, and records
// the outcome last. run does the actual migrating.
func (r *Runner) RunWorker(ctx context.Context, args []string, run func(context.Context, domain.Job, app.Progress) (int, error)) error {
	job, err := domain.ParseJob(args)
	if err != nil {
		return err
	}
	_, logFile := r.paths(job.Ref())
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prog := func(e domain.Event) { fmt.Fprintln(f, FormatEvent(e)) }
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.watchCancel(ctx, job.Ref(), cancel)
	n, runErr := run(ctx, job, prog)

	// Deciding what follows and logging the end happen under the lock, so a
	// Start that sees the run still going queues its job where this worker
	// will find it.
	unlock, lockErr := r.lock(job.Ref())
	if lockErr != nil {
		unlock = func() {}
	}
	var next *domain.Job
	if runErr != nil {
		// The jobs behind a failed or cancelled one may depend on it.
		if dropped := len(r.readQueue(job.Ref())); dropped > 0 {
			fmt.Fprintf(f, "! %d queued job(s) dropped\n", dropped)
			r.writeQueue(job.Ref(), nil) //nolint:errcheck // best effort
		}
		msg := strings.Join(strings.Fields(runErr.Error()), " ")
		word := "failed"
		if ctx.Err() != nil { // cancelled from outside, not failed on its own
			word = "cancelled"
		}
		fmt.Fprintf(f, "%s%s %d %s\n", finishedPrefix, word, n, msg)
		unlock()
		return runErr
	}
	if queue := r.readQueue(job.Ref()); len(queue) > 0 {
		next = &queue[0]
		r.writeQueue(job.Ref(), queue[1:]) //nolint:errcheck // best effort
		fmt.Fprintln(f, nextLine)
	}
	fmt.Fprintf(f, "%sok %d\n", finishedPrefix, n)
	unlock()

	if next != nil {
		time.Sleep(chainGrace)
		if err := r.spawn(*next, true); err != nil {
			fmt.Fprintf(f, "✗ could not start the queued run: %v\n", err)
			return err
		}
	}
	return nil
}

// FormatEvent renders a progress event as one log line.
func FormatEvent(e domain.Event) string {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
	case domain.Queued:
		return fmt.Sprintf("%s%s %s", queuedPrefix, e.Direction, label)
	case domain.Started:
		return fmt.Sprintf("… %s %s", e.Direction, label)
	case domain.Done:
		return fmt.Sprintf("✓ %s %s", e.Direction, label)
	}
	return fmt.Sprintf("✗ %s %s: %s", e.Direction, label, strings.Join(strings.Fields(fmt.Sprint(e.Err)), " "))
}
