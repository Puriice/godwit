package fsmigrations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/puriice/godwit/internal/domain"
)

var (
	fileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)
	nameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)
)

// Source implements app.MigrationSource on the file system. Relative
// migration directories are resolved against Root (the project root).
type Source struct {
	Root    string
	drivers []string
}

// New returns a Source rooted at the project directory. drivers are the
// database drivers migrations may target in "-- +godwit driver:" directives.
func New(root string, drivers []string) *Source { return &Source{Root: root, drivers: drivers} }

// Resolve returns dir as an absolute path.
func (s *Source) Resolve(dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(s.Root, dir)
}

// EnsureDir creates the migrations directory if needed.
func (s *Source) EnsureDir(dir string) error { return os.MkdirAll(s.Resolve(dir), 0o755) }

// Load reads and parses every migration in dir, sorted by version. A missing
// directory yields no migrations.
func (s *Source) Load(dir string) ([]*domain.Migration, error) {
	dir = s.Resolve(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*domain.Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		ver, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: bad version: %w", e.Name(), err)
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("duplicate version %d: %s and %s", ver, prev, e.Name())
		}
		seen[ver] = e.Name()

		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		parsed, err := Parse(string(raw), s.drivers)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out = append(out, &domain.Migration{
			Version:       ver,
			Name:          m[2],
			Source:        path,
			Checksum:      hex.EncodeToString(sum[:]),
			Up:            parsed.Up,
			Down:          parsed.Down,
			NoTransaction: parsed.NoTransaction,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const template = `-- +godwit Up
-- +godwit driver: all

-- +godwit Down
-- +godwit driver: all
`

// Create scaffolds a new migration file in dir and returns its path.
func (s *Source) Create(dir, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid migration name %q (use letters, digits, _ and -)", name)
	}
	dir = s.Resolve(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.sql", time.Now().UTC().Format("20060102150405"), name))
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
