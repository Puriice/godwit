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

	DisabledMigrations []int64 `json:"disabledMigrations,omitempty"`
	EnabledMigrations  []int64 `json:"enabledMigrations,omitempty"`
}

type pluginJSON struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type configJSON struct {
	// Service names this directory as a service of a workspace.
	Service string `json:"service,omitempty"`
	// Services lists a workspace's service directories (root only).
	Services []string `json:"services,omitempty"`

	MigrationsDir string       `json:"migrationsDir"`
	Targets       []targetJSON `json:"targets"`
	Plugins       []pluginJSON `json:"plugins,omitempty"`

	DisabledMigrations []int64 `json:"disabledMigrations,omitempty"`
}

// Store implements app.ProjectStore.
type Store struct {
	root string // directory containing .godwit
	// keyPrefix goes between GODWIT_ and the target in password variable
	// names: "USERS_" for the targets of service users, empty otherwise.
	keyPrefix string

	mu      sync.Mutex
	secrets map[string]string // from .env, plus session-only passwords
}

// New returns a Store for the project (or workspace root) at root.
func New(root string) *Store {
	return &Store{root: root, secrets: map[string]string{}}
}

// NewService returns a Store for the service of a workspace kept in dir. The
// passwords of its targets are named GODWIT_<SERVICE>_<TARGET>_PASSWORD, so
// they never clash with the workspace's global targets or other services.
func NewService(dir, service string) *Store {
	s := New(dir)
	if service != "" {
		s.keyPrefix = envName(service) + "_"
	}
	return s
}

func (s *Store) dir() string { return filepath.Join(s.root, DirName) }

// Load reads the project. A missing .godwit directory yields an empty
// project; nothing is written until Save.
func (s *Store) Load() (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var p domain.Project
	c, found, err := readConfig(s.root)
	switch {
	case err != nil:
		return p, err
	case found:
		p.Service, p.Services = c.Service, c.Services
		p.MigrationsDir = c.MigrationsDir
		p.DisabledMigrations = c.DisabledMigrations
		for _, t := range c.Targets {
			p.Targets = append(p.Targets, domain.Target(t))
		}
		for _, pl := range c.Plugins {
			p.Plugins = append(p.Plugins, domain.PluginSpec(pl))
		}
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
	c := configJSON{Service: p.Service, Services: p.Services, MigrationsDir: p.MigrationsDir, Targets: []targetJSON{}, DisabledMigrations: p.DisabledMigrations}
	for _, t := range p.Targets {
		c.Targets = append(c.Targets, targetJSON(t))
	}
	for _, pl := range p.Plugins {
		c.Plugins = append(c.Plugins, pluginJSON(pl))
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

// readConfig reads the config.json in dir's .godwit directory. found is false
// when there is none.
func readConfig(dir string) (c configJSON, found bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, DirName, configFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c, false, nil
	case err != nil:
		return c, false, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, false, fmt.Errorf("%s: %w", filepath.Join(dir, DirName, configFile), err)
	}
	return c, true, nil
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// envName turns a name into the upper-case, underscore-separated form used in
// environment variable names.
func envName(name string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToUpper(name), "_"), "_")
}

// PasswordKey is the env var name holding the password of a target in a plain
// project, or a global target of a workspace.
func PasswordKey(target string) string {
	return "GODWIT_" + envName(target) + "_PASSWORD"
}

// PasswordKey is the env var name holding the password of one of this store's
// targets.
func (s *Store) PasswordKey(target string) string {
	return "GODWIT_" + s.keyPrefix + envName(target) + "_PASSWORD"
}

// Password resolves a target's password. The process environment overrides
// .env, so CI works without a file.
func (s *Store) Password(target string) (string, bool) {
	key := s.PasswordKey(target)
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

	key := s.PasswordKey(target)
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

	key := s.PasswordKey(target)
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
