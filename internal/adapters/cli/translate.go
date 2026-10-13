package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/puriice/godwit/internal/app"
)

// TranslateUsage documents "godwit translate".
const TranslateUsage = `usage: godwit translate goose [--dry-run] [--table NAME] [target...]

  goose  move a project from pressly/goose to godwit:
           - adds a "-- +godwit" directive next to every "-- +goose" one in the
             migration files; the goose directives stay, so both tools keep working
           - records the versions goose has applied in each target's
             godwit_migration table; the goose table is only read

  --dry-run     show what would change and write nothing
  --table NAME  goose's version table (default goose_db_version; schema.table works)

Without target names, enabled targets are used. Running it again is harmless:
converted files and recorded versions are skipped.
`

// Translate runs "godwit translate ...". args excludes the leading "translate".
func Translate(ctx context.Context, svc *app.Service, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing tool\n\n" + TranslateUsage)
	}
	tool := args[0]
	if !slices.Contains(app.TranslateTools(), tool) {
		return fmt.Errorf("unknown tool %q (available: %s)\n\n%s", tool, strings.Join(app.TranslateTools(), ", "), TranslateUsage)
	}

	var dryRun bool
	table := app.DefaultForeignTable(tool)
	fs := flag.NewFlagSet("translate "+tool, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&dryRun, "dry-run", false, "")
	fs.StringVar(&table, "table", table, "")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("translate %s: %v\n\n%s", tool, err, TranslateUsage)
	}

	names, err := selectTargets(svc, fs.Args())
	if err != nil {
		return err
	}

	files, err := svc.TranslateFiles(tool, dryRun)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "== migration files (%s) ==\n", svc.MigrationsLocation())
	failedFiles := printFiles(out, files, dryRun)

	err = eachTarget(names, out, func(name string) error {
		res, err := svc.TranslateState(ctx, name, tool, table, files, dryRun)
		if err != nil {
			return err
		}
		printState(out, tool, table, res, dryRun)
		return nil
	})
	if failedFiles > 0 {
		ferr := fmt.Errorf("%d migration file(s) could not be converted", failedFiles)
		return errors.Join(ferr, err)
	}
	return err
}

func printFiles(out io.Writer, files []app.TranslatedFile, dryRun bool) (failed int) {
	if len(files) == 0 {
		fmt.Fprintln(out, "no migration files found")
		return 0
	}
	converted := "converted"
	if dryRun {
		converted = "would convert"
	}
	counts := map[app.FileStatus]int{}
	for _, f := range files {
		counts[f.Status]++
		switch f.Status {
		case app.FileConverted:
			fmt.Fprintf(out, "  %s: %s\n", converted, f.File)
		case app.FileFailed:
			failed++
			fmt.Fprintf(out, "  failed: %s: %v\n", f.File, f.Err)
		case app.FileSkipped:
			fmt.Fprintf(out, "  skipped: %s\n", f.File)
		}
		for _, w := range f.Warnings {
			fmt.Fprintf(out, "  warning: %s: %s\n", f.File, w)
		}
	}
	fmt.Fprintf(out, "%d %s, %d already godwit, %d skipped, %d failed\n",
		counts[app.FileConverted], converted, counts[app.FileUnchanged], counts[app.FileSkipped], counts[app.FileFailed])
	return failed
}

func printState(out io.Writer, tool, table string, r app.StateTranslation, dryRun bool) {
	if r.NoTable {
		fmt.Fprintf(out, "no %s table (%s) here; nothing to import\n", tool, table)
		return
	}
	verb := "imported"
	if dryRun {
		verb = "would import"
	}
	fmt.Fprintf(out, "%s %d applied version(s), %d already recorded\n", verb, len(r.Imported), len(r.Existing))
	if len(r.NoFile) > 0 {
		fmt.Fprintf(out, "warning: applied in %s but no usable migration file, skipped: %v\n", tool, r.NoFile)
	}
	if r.Unapplied > 0 {
		fmt.Fprintf(out, "%d version(s) are rolled back in %s and stay pending\n", r.Unapplied, tool)
	}
}
