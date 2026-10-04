// Package config manages the .godwit project directory: non-secret settings in
// config.json, secrets in .env, and the .gitignore that keeps .env out of git.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/joho/godotenv"
)

const (
	DirName        = ".godwit"
	configFile     = "config.json"
	envFile        = ".env"
	gitignoreFile  = ".gitignore"
	defaultMigsDir = "migrations"
)

// Target is a database target. It never holds the password.
type Target struct {
	Name     string            `json:"name"`
	Driver   string            `json:"driver"` // "postgres" | "mysql"
	Host     string            `json:"host"`
	Port     int               `json:"port,omitempty"`
	Database string            `json:"database"`
	User     string            `json:"user"`
	Params   map[string]string `json:"params,omitempty"`
}

// Config is the contents of config.json.
type Config struct {
	MigrationsDir string   `json:"migrationsDir"`
	Targets       []Target `json:"targets"`
}

// Project is a loaded .godwit directory.
type Project struct {
	Root string // directory containing .godwit
	Config
	secrets map[string]string // from .env
}

// Dir returns the .godwit directory path.
func (p *Project) Dir() string { return filepath.Join(p.Root, DirName) }

// MigrationsPath returns the absolute migrations directory.
func (p *Project) MigrationsPath() string {
	d := p.MigrationsDir
	if d == "" {
		d = defaultMigsDir
	}
	if filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(p.Root, d)
}

// Load reads the project at root. A missing .godwit directory yields an empty
// project (nothing is written until Save).
func Load(root string) (*Project, error) {
	p := &Project{Root: root, secrets: map[string]string{}}
	p.MigrationsDir = defaultMigsDir

	raw, err := os.ReadFile(filepath.Join(p.Dir(), configFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &p.Config); err != nil {
			return nil, fmt.Errorf("%s: %w", configFile, err)
		}
		if p.MigrationsDir == "" {
			p.MigrationsDir = defaultMigsDir
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}

	env, err := godotenv.Read(filepath.Join(p.Dir(), envFile))
	switch {
	case err == nil:
		p.secrets = env
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%s: %w", envFile, err)
	}
	return p, nil
}

// Save writes config.json and makes sure .gitignore protects .env.
func (p *Project) Save() error {
	if err := os.MkdirAll(p.Dir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p.Config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.Dir(), configFile), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return p.ensureGitignore()
}

func (p *Project) ensureGitignore() error {
	path := filepath.Join(p.Dir(), gitignoreFile)
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
func PasswordKey(targetName string) string {
	return "GODWIT_" + strings.Trim(nonAlnum.ReplaceAllString(strings.ToUpper(targetName), "_"), "_") + "_PASSWORD"
}

// Password resolves a target's password. The process environment overrides
// .env. ok is false when neither has it (the TUI should then prompt).
func (p *Project) Password(t Target) (pw string, ok bool) {
	key := PasswordKey(t.Name)
	if v, found := os.LookupEnv(key); found {
		return v, true
	}
	v, found := p.secrets[key]
	return v, found
}

// SetPassword stores a password. With persist it is written to .env;
// otherwise it is only kept in memory for this session.
func (p *Project) SetPassword(t Target, pw string, persist bool) error {
	p.secrets[PasswordKey(t.Name)] = pw
	if !persist {
		return nil
	}
	if err := os.MkdirAll(p.Dir(), 0o755); err != nil {
		return err
	}
	// Protect .env before the secret hits disk.
	if err := p.ensureGitignore(); err != nil {
		return err
	}
	path := filepath.Join(p.Dir(), envFile)
	existing, err := godotenv.Read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existing == nil {
		existing = map[string]string{}
	}
	existing[PasswordKey(t.Name)] = pw
	content, err := godotenv.Marshal(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content+"\n"), 0o600)
}

// RemoveTarget deletes the named target and its stored password (in memory
// and in .env), then saves config.json. A password in the process
// environment is not ours to remove.
func (p *Project) RemoveTarget(name string) error {
	i := slices.IndexFunc(p.Targets, func(t Target) bool { return t.Name == name })
	if i < 0 {
		return fmt.Errorf("no target named %q", name)
	}
	p.Targets = slices.Delete(p.Targets, i, i+1)
	key := PasswordKey(name)
	delete(p.secrets, key)

	path := filepath.Join(p.Dir(), envFile)
	existing, err := godotenv.Read(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("%s: %w", envFile, err)
	default:
		if _, found := existing[key]; found {
			delete(existing, key)
			if len(existing) == 0 {
				if err := os.Remove(path); err != nil {
					return err
				}
			} else {
				content, err := godotenv.Marshal(existing)
				if err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
					return err
				}
			}
		}
	}
	return p.Save()
}

// Target returns the named target.
func (p *Project) Target(name string) (Target, bool) {
	for _, t := range p.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// MissingPasswords lists targets that have no resolvable password.
func (p *Project) MissingPasswords() []Target {
	var out []Target
	for _, t := range p.Targets {
		if _, ok := p.Password(t); !ok {
			out = append(out, t)
		}
	}
	return out
}
