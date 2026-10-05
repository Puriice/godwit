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
const MigrateUsage = `usage: godwit migrate status [target...]
       godwit migrate up [-n N | --to VERSION] [target...]
       godwit migrate down [-n N | --to VERSION | --batch] [target...]
       godwit migrate redo <version> [target...]
       godwit migrate clear-dirty <version> <target>
       godwit migrate new <name>

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
  new          create migrations/<timestamp>_<name>.sql from the template

Without target names, enabled targets are used; disabled targets are skipped.
Passwords come from GODWIT_<TARGET>_PASSWORD or .godwit/.env; nothing is
prompted for. Every target is attempted even if an earlier one fails.
`

// Migrate runs "godwit migrate ...". args excludes the leading "migrate".
func Migrate(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	if len(args) > 0 {
		rest := args[1:]
		switch args[0] {
		case "status":
			return migrateStatus(ctx, svc, rest, out)
		case "up":
			return migrateMove(ctx, svc, domain.Up, rest, out)
		case "down":
			return migrateMove(ctx, svc, domain.Down, rest, out)
		case "redo":
			return migrateRedo(ctx, svc, rest, out)
		case "clear-dirty":
			return migrateClearDirty(ctx, svc, rest, out)
		case "new":
			return migrateNew(svc, rest, out)
		}
	}
	return errors.New("unknown migrate command\n\n" + MigrateUsage)
}

// selectTargets resolves explicit names, or every enabled target when none
// are given.
func selectTargets(svc *app.Service, names []string) ([]string, error) {
	if len(names) > 0 {
		for _, n := range names {
			if _, ok := svc.Target(n); !ok {
				return nil, fmt.Errorf("no target named %q", n)
			}
		}
		return names, nil
	}
	var out []string
	for _, t := range svc.Targets() {
		if !t.Disabled {
			out = append(out, t.Name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no enabled targets; add one with: godwit auth add <name> <driver> <connection string>")
	}
	return out, nil
}

// eachTarget runs fn on every target and reports all failures together.
func eachTarget(names []string, out io.Writer, fn func(name string) error) error {
	var failed []string
	for _, n := range names {
		fmt.Fprintf(out, "== %s ==\n", n)
		if err := fn(n); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			failed = append(failed, n)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed on %d target(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

func parseFlags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	define(fs)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("migrate %s: %v\n\n%s", name, err, MigrateUsage)
	}
	return fs.Args(), nil
}

func parseVersion(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid version %q", s)
	}
	return v, nil
}

func migrateStatus(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	names, err := selectTargets(svc, args)
	if err != nil {
		return err
	}
	return eachTarget(names, out, func(name string) error {
		items, err := svc.Status(ctx, name)
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

func migrateMove(ctx context.Context, svc *app.Service, dir domain.Direction, args []string, out io.Writer) error {
	var n int
	var to string
	var batch bool
	rest, err := parseFlags(string(dir), args, func(fs *flag.FlagSet) {
		fs.IntVar(&n, "n", 0, "")
		fs.StringVar(&to, "to", "", "")
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
	names, err := selectTargets(svc, rest)
	if err != nil {
		return err
	}
	prog := func(e domain.Event) { printEvent(out, e) }
	return eachTarget(names, out, func(name string) error {
		var done int
		var err error
		switch {
		case dir == domain.Up && version != 0:
			done, err = svc.UpTo(ctx, name, version, prog)
		case dir == domain.Up:
			done, err = svc.Up(ctx, name, n, prog)
		case batch:
			done, err = svc.DownBatch(ctx, name, prog)
		case version != 0:
			done, err = svc.DownTo(ctx, name, version, prog)
		default:
			done, err = svc.Down(ctx, name, n, prog)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d migration(s) %s\n", done, doneWord(dir))
		return nil
	})
}

func migrateRedo(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("migrate redo needs a version\n\n%s", MigrateUsage)
	}
	version, err := parseVersion(args[0])
	if err != nil {
		return err
	}
	names, err := selectTargets(svc, args[1:])
	if err != nil {
		return err
	}
	prog := func(e domain.Event) { printEvent(out, e) }
	return eachTarget(names, out, func(name string) error {
		if err := svc.Redo(ctx, name, version, prog); err != nil {
			return err
		}
		fmt.Fprintln(out, "1 migration(s) redone")
		return nil
	})
}

func migrateClearDirty(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("migrate clear-dirty takes a version and a target\n\n%s", MigrateUsage)
	}
	version, err := parseVersion(args[0])
	if err != nil {
		return err
	}
	if _, ok := svc.Target(args[1]); !ok {
		return fmt.Errorf("no target named %q", args[1])
	}
	if err := svc.ClearDirty(ctx, args[1], version); err != nil {
		return err
	}
	fmt.Fprintf(out, "Cleared the dirty flag of %d on %q.\n", version, args[1])
	return nil
}

func migrateNew(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("migrate new takes 1 argument (name), got %d\n\n%s", len(args), MigrateUsage)
	}
	path, err := svc.CreateMigration(args[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Created", path)
	return nil
}

func printEvent(out io.Writer, e domain.Event) {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
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
