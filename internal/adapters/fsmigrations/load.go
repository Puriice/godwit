package fsmigrations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
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
	Root      string
	drivers   []string
	normalize func(string) string
}

// New returns a Source rooted at the project directory. drivers are the
// database drivers migrations may target in "-- +godwit driver:" directives.
func New(root string, drivers []string) *Source {
	return &Source{Root: root, drivers: drivers, normalize: domain.NormalizeDriver}
}

// WithNormalizer sets how driver aliases in directives map to driver names
// (the default knows only the built-in aliases) and returns s.
func (s *Source) WithNormalizer(f func(string) string) *Source {
	s.normalize = f
	return s
}

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
// directory yields no migrations. It fails on the first invalid file; use Scan
// to see every file's outcome.
func (s *Source) Load(dir string) ([]*domain.Migration, error) {
	files, err := s.Scan(dir)
	if err != nil {
		return nil, err
	}
	var out []*domain.Migration
	for _, f := range files {
		if f.Err != nil {
			return nil, f.Err
		}
		out = append(out, f.Migration)
	}
	return out, nil
}

// Scan reads and parses every migration file in dir, sorted by version, then
// name (files with an unusable version come last). A file that cannot be used
// is returned with Err set rather than aborting the scan. A missing directory
// yields no files.
func (s *Source) Scan(dir string) ([]domain.MigrationFile, error) {
	dir = s.Resolve(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type scanned struct {
		ver  int64
		file domain.MigrationFile
	}
	var found []scanned
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		fail := func(ver int64, err error) {
			found = append(found, scanned{ver, domain.MigrationFile{Name: e.Name(), Err: err}})
		}
		ver, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			fail(math.MaxInt64, fmt.Errorf("%s: bad version: %w", e.Name(), err))
			continue
		}
		if prev, dup := seen[ver]; dup {
			fail(ver, fmt.Errorf("duplicate version %d: %s and %s", ver, prev, e.Name()))
			continue
		}
		seen[ver] = e.Name()

		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(ver, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		parsed, err := ParseWith(string(raw), s.drivers, s.norm())
		if err != nil {
			fail(ver, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		sum := sha256.Sum256(raw)
		found = append(found, scanned{ver, domain.MigrationFile{Name: e.Name(), Migration: &domain.Migration{
			Version:       ver,
			Name:          m[2],
			Source:        path,
			Checksum:      hex.EncodeToString(sum[:]),
			Up:            parsed.Up,
			Down:          parsed.Down,
			NoTransaction: parsed.NoTransaction,
		}}})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].ver != found[j].ver {
			return found[i].ver < found[j].ver
		}
		return found[i].file.Name < found[j].file.Name
	})
	out := make([]domain.MigrationFile, len(found))
	for i, f := range found {
		out[i] = f.file
	}
	return out, nil
}

// Raw lists the .sql and .go files in dir, unparsed, sorted by name. A missing
// directory yields none.
func (s *Source) Raw(dir string) ([]domain.RawFile, error) {
	dir = s.Resolve(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.RawFile
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".sql" && ext != ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, domain.RawFile{Name: e.Name(), Content: string(b)})
	}
	return out, nil
}

// Write replaces the contents of the existing file name in dir.
func (s *Source) Write(dir, name, content string) error {
	if name != filepath.Base(name) {
		return fmt.Errorf("invalid file name %q", name)
	}
	path := filepath.Join(s.Resolve(dir), name)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), info.Mode().Perm())
}

// Check parses content as a migration and returns the checksum Scan would
// record for a file holding exactly that content.
func (s *Source) Check(content string) (string, error) {
	if _, err := ParseWith(content, s.drivers, s.norm()); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:]), nil
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

func (s *Source) norm() func(string) string {
	if s.normalize == nil {
		return domain.NormalizeDriver
	}
	return s.normalize
}
