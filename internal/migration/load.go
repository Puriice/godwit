package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var fileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)

// Migration is a parsed migration file.
type Migration struct {
	Version  int64
	Name     string
	Path     string
	Checksum string // sha256 hex of the raw file
	Parsed   *Parsed
}

// Load reads and parses every migration in dir, sorted by version.
func Load(dir string) ([]*Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		var ver int64
		if _, err := fmt.Sscanf(m[1], "%d", &ver); err != nil {
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
		parsed, err := Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out = append(out, &Migration{
			Version:  ver,
			Name:     m[2],
			Path:     path,
			Checksum: hex.EncodeToString(sum[:]),
			Parsed:   parsed,
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
func Create(dir, name string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_\-]+$`).MatchString(name) {
		return "", fmt.Errorf("invalid migration name %q (use letters, digits, _ and -)", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.sql", time.Now().UTC().Format("20060102150405"), name))
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
