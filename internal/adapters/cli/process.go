package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// ProcessUsage documents the process subcommands.
const ProcessUsage = `usage: godwit process list [target...]
       godwit process show <target> [-n LINES]
       godwit process cancel <target>
       godwit process dequeue <target> <position>
       godwit process clear-queue <target>

  list         show each target's latest background run and the jobs queued behind it
  show         details of one target's run, its queue and the tail of its log
                 -n LINES     log lines to show (default 20, 0 = all)
  cancel       stop the run in progress; the migration being executed is stopped where
               it is: a transaction is rolled back, but finished passes of a repeat
               block stay and the migration is left dirty
  dequeue      remove one queued job; position is the number "process show" prints
  clear-queue  remove every job queued behind the run (the run is not affected)

Runs are started with "godwit migrate up|down|redo --detach". Nothing is asked
for confirmation. Without target names, every target with a run is listed.
`

// Process runs "godwit process ...". args excludes the leading "process".
func Process(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if len(args) > 0 {
		rest := args[1:]
		switch args[0] {
		case "list":
			return processList(svc, bg, rest, out)
		case "show":
			return processShow(svc, bg, rest, out)
		case "cancel":
			return processCancel(svc, bg, rest, out)
		case "dequeue":
			return processDequeue(svc, bg, rest, out)
		case "clear-queue":
			return processClearQueue(svc, bg, rest, out)
		}
	}
	return errors.New("unknown process command\n\n" + ProcessUsage)
}

func processTarget(svc *app.Service, bg app.BackgroundRunner, cmd string, args []string, want int) error {
	if len(args) != want {
		return fmt.Errorf("process %s takes %d argument(s), got %d\n\n%s", cmd, want, len(args), ProcessUsage)
	}
	if bg == nil {
		return errors.New("background runs are not available here")
	}
	if _, ok := svc.Target(args[0]); !ok {
		return fmt.Errorf("no target named %q", args[0])
	}
	return nil
}

// jobLabel is a short description of a job: "up -n 2", "redo 7".
func jobLabel(j domain.Job) string {
	switch {
	case j.Version != 0:
		return fmt.Sprintf("%s %d", j.Op, j.Version)
	case j.N != 0:
		return fmt.Sprintf("%s -n %d", j.Op, j.N)
	}
	return string(j.Op)
}

// runState says how a run is doing.
func runState(rs app.RunStatus) string {
	switch {
	case rs.Active && rs.Cancelling:
		return "cancelling"
	case rs.Active:
		return "running"
	case rs.Done && rs.Cancelled:
		return "cancelled"
	case rs.Done && rs.Err != "":
		return "failed"
	case rs.Done:
		return "finished"
	}
	return "stopped" // the worker is gone without recording an outcome
}

// runProgress is "done/total", or empty when the run announced nothing.
func runProgress(rs app.RunStatus) string {
	if len(rs.Queued) == 0 || rs.Job.Op == domain.OpRedo {
		return ""
	}
	return fmt.Sprintf("%d/%d", len(rs.Finished), len(rs.Queued))
}

func processList(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if bg == nil {
		return errors.New("background runs are not available here")
	}
	var names []string
	if len(args) > 0 {
		for _, n := range args {
			if _, ok := svc.Target(n); !ok {
				return fmt.Errorf("no target named %q", n)
			}
		}
		names = args
	} else {
		for _, t := range svc.Targets() {
			names = append(names, t.Name)
		}
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	shown := 0
	for _, n := range names {
		rs, err := bg.Poll(n, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if rs.Job.Op == "" {
			continue
		}
		shown++
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n, rs.Job.Op, runState(rs), runProgress(rs))
		for i, j := range rs.Pending {
			fmt.Fprintf(w, "  ↳ %d\t%s\tqueued\t\n", i+1, jobLabel(j))
		}
	}
	if shown == 0 {
		fmt.Fprintln(out, "No background runs. Start one with: godwit migrate up --detach")
		return nil
	}
	return w.Flush()
}

func processShow(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lines := fs.Int("n", 20, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("process show: %v\n\n%s", err, ProcessUsage)
	}
	if *lines < 0 {
		return fmt.Errorf("invalid -n %d", *lines)
	}
	rest := fs.Args()
	if err := processTarget(svc, bg, "show", rest, 1); err != nil {
		return err
	}
	name := rest[0]
	rs, err := bg.Poll(name, 0)
	if err != nil {
		return err
	}
	if rs.Job.Op == "" {
		fmt.Fprintf(out, "%s has no background run.\n", name)
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "target\t%s\n", name)
	fmt.Fprintf(w, "operation\t%s\n", jobLabel(rs.Job))
	fmt.Fprintf(w, "status\t%s\n", runState(rs))
	if rs.Active {
		fmt.Fprintf(w, "process\tpid %d (background)\n", rs.Pid)
	}
	if p := runProgress(rs); p != "" {
		fmt.Fprintf(w, "progress\t%s\n", p)
	}
	if rs.Err != "" {
		fmt.Fprintf(w, "error\t%s\n", firstLine(rs.Err))
	}
	fmt.Fprintf(w, "log\t%s\n", bg.LogPath(name))
	if err := w.Flush(); err != nil {
		return err
	}
	if len(rs.Pending) > 0 {
		fmt.Fprintln(out, "\nqueued (starts when the run before it ends well; dropped if it fails or is cancelled):")
		for i, j := range rs.Pending {
			fmt.Fprintf(out, "  %d  %s\n", i+1, jobLabel(j))
		}
	}
	log := rs.Lines
	if *lines > 0 && len(log) > *lines {
		log = log[len(log)-*lines:]
	}
	if len(log) > 0 {
		fmt.Fprintln(out, "\nlog:")
		for _, l := range log {
			fmt.Fprintf(out, "  %s\n", l)
		}
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func processCancel(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if err := processTarget(svc, bg, "cancel", args, 1); err != nil {
		return err
	}
	rs, err := bg.Poll(args[0], 0)
	if err != nil {
		return err
	}
	switch {
	case !rs.Active:
		return fmt.Errorf("%s has no run in progress", args[0])
	case rs.Cancelling:
		fmt.Fprintf(out, "Already cancelling the run on %s.\n", args[0])
		return nil
	}
	if err := bg.Stop(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(out, "Cancel requested for the %s run on %s. Check it with: godwit process show %s\n", rs.Job.Op, args[0], args[0])
	return nil
}

func processDequeue(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if err := processTarget(svc, bg, "dequeue", args, 2); err != nil {
		return err
	}
	pos, err := strconv.Atoi(args[1])
	if err != nil || pos < 1 {
		return fmt.Errorf("invalid position %q; use the number \"process show\" prints", args[1])
	}
	rs, err := bg.Poll(args[0], 0)
	if err != nil {
		return err
	}
	if pos > len(rs.Pending) {
		return fmt.Errorf("%s has %d queued job(s); there is no position %d", args[0], len(rs.Pending), pos)
	}
	job := rs.Pending[pos-1]
	if err := bg.Dequeue(args[0], pos-1, job); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s from the queue of %s.\n", jobLabel(job), args[0])
	return nil
}

func processClearQueue(svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if err := processTarget(svc, bg, "clear-queue", args, 1); err != nil {
		return err
	}
	n, err := bg.ClearQueue(args[0])
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintf(out, "Nothing is queued on %s.\n", args[0])
		return nil
	}
	fmt.Fprintf(out, "Removed %d queued job(s) from %s. The run in progress is not affected.\n", n, args[0])
	return nil
}
