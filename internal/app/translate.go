package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/puriice/godwit/internal/domain"
)

// TranslateTools lists the migration tools "translate" understands.
func TranslateTools() []string { return []string{"goose"} }

// DefaultForeignTable is the version table a tool keeps by default.
func DefaultForeignTable(tool string) string {
	if tool == "goose" {
		return "goose_db_version"
	}
	return ""
}

// ErrUnknownTool is returned for a tool "translate" does not know.
var ErrUnknownTool = errors.New("unknown tool")

// FileStatus says what translating one migration file did.
type FileStatus string

const (
	FileConverted FileStatus = "converted" // godwit directives were added
	FileUnchanged FileStatus = "unchanged" // already a valid godwit migration
	FileSkipped   FileStatus = "skipped"   // not something godwit can use
	FileFailed    FileStatus = "failed"    // converting it gave an invalid migration
)

// TranslatedFile is the outcome for one file in the migrations directory.
type TranslatedFile struct {
	File     string
	Version  int64
	Name     string
	Checksum string // of the translated contents; empty unless usable
	Status   FileStatus
	Warnings []string
	Err      error
}

// TranslateFiles adds the godwit directives to every migration file written for
// tool, keeping the tool's own directives. With dryRun nothing is written.
func (s *Service) TranslateFiles(tool string, dryRun bool) ([]TranslatedFile, error) {
	if tool != "goose" {
		return nil, fmt.Errorf("%w %q (available: %s)", ErrUnknownTool, tool, strings.Join(TranslateTools(), ", "))
	}
	dir := s.MigrationsDir()
	raw, err := s.source.Raw(dir)
	if err != nil {
		return nil, err
	}
	var out []TranslatedFile
	for _, f := range raw {
		res := TranslatedFile{File: f.Name}
		if strings.HasSuffix(f.Name, ".go") {
			res.Status = FileSkipped
			res.Warnings = []string{"Go migrations are not supported"}
			out = append(out, res)
			continue
		}
		v, name, ok := domain.ParseMigrationFileName(f.Name)
		if !ok {
			res.Status = FileSkipped
			res.Warnings = []string{"file name is not <version>_<name>.sql (letters, digits, _ and -); godwit ignores it"}
			out = append(out, res)
			continue
		}
		res.Version, res.Name = v, name

		text, warns, changed := domain.ConvertGoose(f.Content)
		res.Warnings = warns
		sum, err := s.source.Check(text)
		switch {
		case err != nil:
			res.Status, res.Err = FileFailed, err
		case changed:
			res.Status, res.Checksum = FileConverted, sum
			if !dryRun {
				if err := s.source.Write(dir, f.Name, text); err != nil {
					res.Status, res.Err, res.Checksum = FileFailed, err, ""
				}
			}
		default:
			res.Status, res.Checksum = FileUnchanged, sum
		}
		out = append(out, res)
	}
	return out, nil
}

// StateTranslation is the outcome of importing one tool's history into a target.
type StateTranslation struct {
	NoTable   bool    // the tool's version table does not exist on the target
	Imported  []int64 // versions recorded in godwit_migration
	Existing  []int64 // already recorded there
	NoFile    []int64 // applied for the tool, but no usable migration file
	Unapplied int     // versions the tool has rolled back or never applied
}

// TranslateState records the versions tool has applied on target in its
// godwit_migration table, with the checksums of the translated files. The
// tool's own table is only read. With dryRun nothing is written.
func (s *Service) TranslateState(ctx context.Context, target, tool, table string, files []TranslatedFile, dryRun bool) (res StateTranslation, err error) {
	if tool != "goose" {
		return res, fmt.Errorf("%w %q", ErrUnknownTool, tool)
	}
	db, t, err := s.open(ctx, target)
	if err != nil {
		return res, err
	}
	defer db.Close()
	imp, ok := db.(ForeignImporter)
	if !ok {
		return res, fmt.Errorf("the %s driver cannot read a %s table", t.Driver, tool)
	}

	rows, found, err := imp.ForeignApplied(ctx, tool, table)
	if err != nil {
		return res, err
	}
	if !found {
		res.NoTable = true
		return res, nil
	}

	var existing []domain.Record
	if dryRun {
		existing, _ = db.Applied(ctx) // the table may not exist yet
	} else {
		if err = db.EnsureTable(ctx); err != nil {
			return res, err
		}
		var unlock func() error
		if unlock, err = db.Lock(ctx); err != nil {
			return res, err
		}
		defer func() {
			if uerr := unlock(); err == nil {
				err = uerr
			}
		}()
		if existing, err = db.Applied(ctx); err != nil {
			return res, err
		}
	}
	have := map[int64]bool{}
	for _, r := range existing {
		have[r.Version] = true
	}
	byVersion := map[int64]TranslatedFile{}
	for _, f := range files {
		if f.Checksum != "" {
			byVersion[f.Version] = f
		}
	}

	applied := domain.GooseApplied(rows)
	seen := map[int64]bool{}
	for _, r := range rows {
		seen[r.Version] = true
	}
	res.Unapplied = len(seen) - len(applied)
	if seen[0] {
		res.Unapplied-- // the baseline row is not a migration
	}

	versions := make([]int64, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	slices.Sort(versions)
	var recs []domain.Record
	for _, v := range versions {
		f, ok := byVersion[v]
		switch {
		case have[v]:
			res.Existing = append(res.Existing, v)
		case !ok:
			res.NoFile = append(res.NoFile, v)
		default:
			res.Imported = append(res.Imported, v)
			recs = append(recs, domain.Record{Version: v, Name: f.Name, Checksum: f.Checksum, AppliedAt: applied[v]})
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Version < recs[j].Version })
	if dryRun || len(recs) == 0 {
		return res, nil
	}
	if _, err := imp.ImportRecords(ctx, recs); err != nil {
		return res, err
	}
	return res, nil
}
