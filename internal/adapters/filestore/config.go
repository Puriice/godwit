// Package filestore is the driven adapter that keeps project configuration in
// a .godwit directory: non-secret settings in config.json, secrets in .env,
// and a .gitignore that keeps .env out of version control.
package filestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/joho/godotenv"

	"github.com/puriice/godwit/internal/domain"
)

const (
	// DirName is the project directory this adapter manages.
	DirName       = ".godwit"
	configFile    = "config.json"
	envFile       = ".env"
	gitignoreFile = ".gitignore"
)

// On-disk shapes, kept separate from the domain types so the file format can
// evolve independently.
type targetJSON struct {
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Host     string            `json:"host"`
	Port     int               `json:"port,omitempty"`
	Database string            `json:"database"`
	User     string            `json:"user"`
	Params   map[string]string `json:"params,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

type configJSON struct {
	MigrationsDir string       `json:"migrationsDir"`
	Targets       []targetJSON `json:"targets"`
}

// Store implements app.ProjectStore.
type Store struct {
	root string // directory containing .godwit

	mu      sync.Mutex
	secrets map[string]string // from .env, plus session-only passwords
}

// New returns a Store for the project at root.
func New(root string) *Store {
	return &Store{root: root, secrets: map[string]string{}}
}

func (s *Store) dir() string { return filepath.Join(s.root, DirName) }

// Load reads the project. A missing .godwit directory yields an empty
// project; nothing is written until Save.
func (s *Store) Load() (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var p domain.Project
	raw, err := os.ReadFile(filepath.Join(s.dir(), configFile))
	switch {
	case err == nil:
		var c configJSON
		if err := json.Unmarshal(raw, &c); err != nil {
			return p, fmt.Errorf("%s: %w", configFile, err)
		}
		p.MigrationsDir = c.MigrationsDir
		for _, t := range c.Targets {
			p.Targets = append(p.Targets, domain.Target(t))
		}
	case !errors.Is(err, os.ErrNotExist):
		return p, err
	}

	env, err := godotenv.Read(filepath.Join(s.dir(), envFile))
	switch {
	case err == nil:
		s.secrets = env
	case !errors.Is(err, os.ErrNotExist):
		return p, fmt.Errorf("%s: %w", envFile, err)
	}
	return p, nil
}

// Save writes config.json and makes sure .gitignore protects .env.
func (s *Store) Save(p domain.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	c := configJSON{MigrationsDir: p.MigrationsDir, Targets: []targetJSON{}}
	for _, t := range p.Targets {
		c.Targets = append(c.Targets, targetJSON(t))
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.dir(), configFile), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return s.ensureGitignore()
}

func (s *Store) ensureGitignore() error {
	path := filepath.Join(s.dir(), gitignoreFile)
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for l := range strings.SplitSeq(string(raw), "\n") {
		if strings.TrimSpace(l) == envFile {
			return nil
		}
	}
	out := string(raw)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out+envFile+"\n"), 0o644)
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// PasswordKey is the env var name holding a target's password.
func PasswordKey(target string) string {
	return "GODWIT_" + strings.Trim(nonAlnum.ReplaceAllString(strings.ToUpper(target), "_"), "_") + "_PASSWORD"
}

// Password resolves a target's password. The process environment overrides
// .env, so CI works without a file.
func (s *Store) Password(target string) (string, bool) {
	key := PasswordKey(target)
	if v, found := os.LookupEnv(key); found {
		return v, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, found := s.secrets[key]
	return v, found
}

// SetPassword stores a password. With persist it is written to .env;
// otherwise it is only kept in memory for this session.
func (s *Store) SetPassword(target, pw string, persist bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := PasswordKey(target)
	s.secrets[key] = pw
	if !persist {
		return nil
	}
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	// Protect .env before the secret hits disk.
	if err := s.ensureGitignore(); err != nil {
		return err
	}
	path := filepath.Join(s.dir(), envFile)
	existing, err := godotenv.Read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existing == nil {
		existing = map[string]string{}
	}
	existing[key] = pw
	return writeEnv(path, existing)
}

// DeletePassword forgets a target's password, in memory and in .env. A
// password in the process environment is not ours to remove.
func (s *Store) DeletePassword(target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := PasswordKey(target)
	delete(s.secrets, key)

	path := filepath.Join(s.dir(), envFile)
	existing, err := godotenv.Read(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%s: %w", envFile, err)
	}
	if _, found := existing[key]; !found {
		return nil
	}
	delete(existing, key)
	if len(existing) == 0 {
		return os.Remove(path)
	}
	return writeEnv(path, existing)
}

func writeEnv(path string, vars map[string]string) error {
	content, err := godotenv.Marshal(vars)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content+"\n"), 0o600)
}
