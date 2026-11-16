package cli

import (
	"context"
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

// MigrateUsage documents the migrate subcommands.
const MigrateUsage = `usage: godwit migrate status [-s SERVICE] [target...]
       godwit migrate up [-s SERVICE] [-d | --detach] [-n N | --to VERSION] [target...]
       godwit migrate down [-s SERVICE] [-d | --detach] [-n N | --to VERSION | --batch] [target...]
       godwit migrate redo [-s SERVICE] [-d | --detach] <version> [target...]
       godwit migrate clear-dirty [-s SERVICE] <version> <target>
       godwit migrate new [-s SERVICE] <name>

  status       show every migration's state on each target
  up           apply pending migrations (all by default)
                 -n N         apply at most N
                 --to VERSION apply up to and including VERSION
  down         roll back the newest applied migration (default -n 1)
                 -n N         roll back N
                 --to VERSION roll back everything newer than VERSION
                 --batch      roll back the latest batch: every migration applied
                              in the same run as the newest applied one
  redo         roll back one applied migration and apply it again
  clear-dirty  clear the dirty flag after repairing a failed migration by hand
  new          create <migrations dir>/<timestamp>_<name>.sql from the template;
               at a workspace root with several services, -s names the service

-d or --detach (up, down and redo; put flags before the version or target names)
starts the run in a background process and returns at once. The run keeps going
after this command, or the terminal, exits. Follow it in the log file the command
prints, with "godwit migrate status", or in the TUI. A run that is still going
shows its migrations as running there.

Without target names, enabled targets are used; disabled targets are skipped.
Passwords come from GODWIT_<TARGET>_PASSWORD (GODWIT_<SERVICE>_<TARGET>_PASSWORD
for a service's own targets) or .godwit/.env; nothing is prompted for. Every
target is attempted even if an earlier one fails.

` + ServiceFlagUsage

// Migrate runs "godwit migrate ...". args excludes the leading "migrate".
func Migrate(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	return MigrateWith(ctx, svc, nil, args, out)
}

// MigrateWith is Migrate with a background runner, which --detach needs.
func MigrateWith(ctx context.Context, svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	if len(args) > 0 {
		rest := args[1:]
		switch args[0] {
		case "status":
			return migrateStatus(ctx, svc, rest, out)
		case "up":
			return migrateMove(ctx, svc, bg, domain.Up, rest, out)
		case "down":
			return migrateMove(ctx, svc, bg, domain.Down, rest, out)
		case "redo":
			return migrateRedo(ctx, svc, bg, rest, out)
		case "clear-dirty":
			return migrateClearDirty(ctx, svc, rest, out)
		case "new":
			return migrateNew(svc, rest, out)
		}
	}
	return errors.New("unknown migrate command\n\n" + MigrateUsage)
}

func parseFlags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	return parseCmdFlags("migrate "+name, MigrateUsage, args, define)
}

func parseVersion(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid version %q", s)
	}
	return v, nil
}

func migrateStatus(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	var services serviceFlag
	args, err := parseFlags("status", args, services.define)
	if err != nil {
		return err
	}
	refs, err := selectRefs(svc, services, args)
	if err != nil {
		return err
	}
	if err := reportBrokenMigrations(svc, refServices(refs), out); err != nil {
		return err
	}
	return eachTarget(refs, out, func(ref domain.TargetRef) error {
		items, err := svc.Status(ctx, ref)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "no migrations")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, it := range items {
			note := ""
			if it.Reason != "" {
				note = "(" + it.Reason + ")"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", it.Version, it.Name, it.State, note)
		}
		return w.Flush()
	})
}

// reportBrokenMigrations lists, for each service, every migration file with
// each failure beneath its file, once, and fails if any file is invalid. A
// service whose files all load (or whose directory cannot be listed, which
// Status reports) prints nothing.
func reportBrokenMigrations(svc *app.Service, services []string, out io.Writer) error {
	broken, total := 0, 0
	for _, service := range services {
		files, err := svc.MigrationFiles(service)
		if err != nil {
			continue
		}
		bad := 0
		for _, f := range files {
			if f.Err != nil {
				bad++
			}
		}
		if bad == 0 {
			continue
		}
		broken, total = broken+bad, total+len(files)
		fmt.Fprintln(out, filesHeading(svc, service))
		for _, f := range files {
			if f.Err == nil {
				fmt.Fprintf(out, "%s\n", f.Name)
				continue
			}
			fmt.Fprintf(out, "%s  ERROR\n", f.Name)
			for line := range strings.SplitSeq(f.Err.Error(), "\n") {
				fmt.Fprintf(out, "    %s\n", line)
			}
		}
	}
	if broken == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d migration file(s) have errors", broken, total)
}

// detachJobs starts a background worker per target and returns without
// waiting for any of them; they keep running after this process exits.
func detachJobs(svc *app.Service, bg app.BackgroundRunner, refs []domain.TargetRef, out io.Writer, job func(ref domain.TargetRef) domain.Job) error {
	if bg == nil {
		return errors.New("--detach is not available here")
	}
	return eachTarget(refs, out, func(ref domain.TargetRef) error {
		if svc.NeedsPassword(ref) && !svc.HasPassword(ref) {
			return fmt.Errorf("no password for %q; set %s or add it to .godwit/.env", ref.String(), svc.PasswordKey(ref))
		}
		j := job(ref)
		j.Service, j.Target = ref.Service, ref.Target
		queued, err := bg.Start(j)
		if err != nil {
			return err
		}
		if queued {
			fmt.Fprintf(out, "queued behind the run in progress; it starts when that run ends well. progress: %s\n", bg.LogPath(ref))
			return nil
		}
		fmt.Fprintf(out, "started in the background; progress: %s\n", bg.LogPath(ref))
		return nil
	})
}

func migrateMove(ctx context.Context, svc *app.Service, bg app.BackgroundRunner, dir domain.Direction, args []string, out io.Writer) error {
	var n int
	var to string
	var batch, detach bool
	var services serviceFlag
	rest, err := parseFlags(string(dir), args, func(fs *flag.FlagSet) {
		fs.IntVar(&n, "n", 0, "")
		fs.StringVar(&to, "to", "", "")
		fs.BoolVar(&detach, "detach", false, "")
		fs.BoolVar(&detach, "d", false, "")
		services.define(fs)
		if dir == domain.Down {
			fs.BoolVar(&batch, "batch", false, "")
		}
	})
	if err != nil {
		return err
	}
	if n < 0 {
		return errors.New("-n must be positive")
	}
	if n > 0 && to != "" {
		return errors.New("use either -n or --to, not both")
	}
	if batch && (n > 0 || to != "") {
		return errors.New("--batch cannot be combined with -n or --to")
	}
	var version int64
	if to != "" {
		if version, err = parseVersion(to); err != nil {
			return err
		}
	}
	refs, err := selectRefs(svc, services, rest)
	if err != nil {
		return err
	}
	if detach {
		return detachJobs(svc, bg, refs, out, func(domain.TargetRef) domain.Job {
			j := domain.Job{N: n, Version: version}
			switch {
			case dir == domain.Up && version != 0:
				j.Op = domain.OpUpTo
			case dir == domain.Up:
				j.Op = domain.OpUp
			case batch:
				j.Op = domain.OpBatch
			case version != 0:
				j.Op = domain.OpDownTo
			default:
				j.Op = domain.OpDown
			}
			return j
		})
	}
	prog := func(e domain.Event) { printEvent(out, e) }
	return eachTarget(refs, out, func(ref domain.TargetRef) error {
		var done int
		var err error
		switch {
		case dir == domain.Up && version != 0:
			done, err = svc.UpTo(ctx, ref, version, prog)
		case dir == domain.Up:
			done, err = svc.Up(ctx, ref, n, prog)
		case batch:
			done, err = svc.DownBatch(ctx, ref, prog)
		case version != 0:
			done, err = svc.DownTo(ctx, ref, version, prog)
		default:
			done, err = svc.Down(ctx, ref, n, prog)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d migration(s) %s\n", done, doneWord(dir))
		return nil
	})
}

func migrateRedo(ctx context.Context, svc *app.Service, bg app.BackgroundRunner, args []string, out io.Writer) error {
	var detach bool
	var services serviceFlag
	args, err := parseFlags("redo", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&detach, "detach", false, "")
		fs.BoolVar(&detach, "d", false, "")
		services.define(fs)
	})
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("migrate redo needs a version\n\n%s", MigrateUsage)
	}
	version, err := parseVersion(args[0])
	if err != nil {
		return err
	}
	refs, err := selectRefs(svc, services, args[1:])
	if err != nil {
		return err
	}
	if detach {
		return detachJobs(svc, bg, refs, out, func(domain.TargetRef) domain.Job {
			return domain.Job{Op: domain.OpRedo, Version: version}
		})
	}
	prog := func(e domain.Event) { printEvent(out, e) }
	return eachTarget(refs, out, func(ref domain.TargetRef) error {
		if err := svc.Redo(ctx, ref, version, prog); err != nil {
			return err
		}
		fmt.Fprintln(out, "1 migration(s) redone")
		return nil
	})
}

func migrateClearDirty(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	var services serviceFlag
	args, err := parseFlags("clear-dirty", args, services.define)
	if err != nil {
		return err
	}
	if len(args) != 2 {
		return fmt.Errorf("migrate clear-dirty takes a version and a target\n\n%s", MigrateUsage)
	}
	version, err := parseVersion(args[0])
	if err != nil {
		return err
	}
	ref, err := selectRef(svc, services, args[1])
	if err != nil {
		return err
	}
	if err := svc.ClearDirty(ctx, ref, version); err != nil {
		return err
	}
	fmt.Fprintf(out, "Cleared the dirty flag of %d on %q.\n", version, ref.String())
	return nil
}

// newMigrationService is the service "migrate new" creates a file for: the
// one named with -s, else the current one, else the only one.
func newMigrationService(svc *app.Service, services serviceFlag) (string, error) {
	service, err := oneScope(svc, services)
	if err != nil || service != "" || !svc.IsWorkspace() {
		return service, err
	}
	all := svc.Services()
	if len(all) == 1 {
		return all[0], nil
	}
	return "", fmt.Errorf("migrate new at the workspace root needs a service: -s <service> (services: %s)", strings.Join(all, ", "))
}

func migrateNew(svc *app.Service, args []string, out io.Writer) error {
	var services serviceFlag
	args, err := parseFlags("new", args, services.define)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("migrate new takes 1 argument (name), got %d\n\n%s", len(args), MigrateUsage)
	}
	service, err := newMigrationService(svc, services)
	if err != nil {
		return err
	}
	path, err := svc.CreateMigration(service, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Created", path)
	return nil
}

func printEvent(out io.Writer, e domain.Event) {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
	case domain.Queued:
	case domain.Started:
		fmt.Fprintf(out, "… %s %s\n", e.Direction, label)
	case domain.Done:
		fmt.Fprintf(out, "✓ %s %s\n", e.Direction, label)
	default:
		fmt.Fprintf(out, "✗ %s %s: %v\n", e.Direction, label, e.Err)
	}
}

func doneWord(d domain.Direction) string {
	if d == domain.Up {
		return "applied"
	}
	return "reverted"
}
