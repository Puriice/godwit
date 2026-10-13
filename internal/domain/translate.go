package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RawFile is a file in the migrations directory, unparsed.
type RawFile struct {
	Name    string
	Content string
}

// ForeignRow is one row of another tool's version table, in the order the tool
// wrote them.
type ForeignRow struct {
	Version int64
	Applied bool
	At      time.Time
}

var migrationFileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)

// ParseMigrationFileName splits "<version>_<name>.sql" the way godwit reads
// migration file names. ok is false for names godwit ignores.
func ParseMigrationFileName(file string) (version int64, name string, ok bool) {
	m := migrationFileRe.FindStringSubmatch(file)
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return v, m[2], true
}

const (
	gooseMarker  = "-- +goose"
	godwitMarker = "-- +godwit"
)

// ConvertGoose adds godwit directives next to the goose ones in a migration
// file, keeping every goose line so both tools can read the file. A file that
// already has godwit directives is returned unchanged, so converting twice is
// harmless. warnings lists goose features godwit cannot express.
func ConvertGoose(src string) (out string, warnings []string, changed bool) {
	lines := strings.Split(src, "\n")
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), godwitMarker) {
			return src, nil, false
		}
	}

	var b strings.Builder
	for i, line := range lines {
		last := i == len(lines)-1
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), gooseMarker) {
			b.WriteString(line)
			if !last {
				b.WriteByte('\n')
			}
			continue
		}
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		fields := strings.Fields(strings.ToLower(trimmed[len(gooseMarker):]))
		directive := ""
		switch {
		case len(fields) == 0:
		case fields[0] == "no" && len(fields) > 1 && fields[1] == "transaction":
			directive = "NoTransaction"
		case fields[0] == "up", fields[0] == "down", fields[0] == "statementbegin", fields[0] == "statementend":
			directive = map[string]string{"up": "Up", "down": "Down", "statementbegin": "StatementBegin", "statementend": "StatementEnd"}[fields[0]]
		case fields[0] == "envsub":
			warnings = append(warnings, "line "+strconv.Itoa(i+1)+": ENVSUB has no godwit equivalent; left as is")
		}
		if directive == "" {
			b.WriteString(line)
			if !last {
				b.WriteByte('\n')
			}
			continue
		}
		changed = true
		added := godwitMarker + " " + directive + cr
		// StatementEnd closes godwit's raw block first, so the goose line that
		// follows is a plain comment outside it; every other directive goes after
		// its goose line.
		first, second := line, added
		if directive == "StatementEnd" {
			first, second = added, line
		}
		b.WriteString(first)
		b.WriteByte('\n')
		b.WriteString(second)
		if !last {
			b.WriteByte('\n')
		}
	}
	return b.String(), warnings, changed
}

// GooseApplied replays rows of a goose version table (oldest first) and returns
// the versions that are applied, with when they were. Goose records a new row for
// every up and down, so the latest row of a version decides. Version 0 is
// goose's baseline row and is not a migration.
func GooseApplied(rows []ForeignRow) map[int64]time.Time {
	out := map[int64]time.Time{}
	for _, r := range rows {
		if r.Version == 0 {
			continue
		}
		if r.Applied {
			out[r.Version] = r.At
		} else {
			delete(out, r.Version)
		}
	}
	return out
}
