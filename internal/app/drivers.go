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

// DriverDescriber is optionally implemented by a DatabaseFactory whose drivers
// are not built in (plugins). It tells godwit how their connection strings
// look and which aliases they answer to.
type DriverDescriber interface {
	DriverInfos() []domain.DriverInfo
}

// Combine merges factories into one. A driver is opened by the first factory
// that lists it.
func Combine(factories ...DatabaseFactory) DatabaseFactory {
	return combined(factories)
}

type combined []DatabaseFactory

func (c combined) Drivers() []string {
	var names []string
	for _, f := range c {
		for _, n := range f.Drivers() {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (c combined) Open(ctx context.Context, t domain.Target, password string) (Database, error) {
	name := strings.ToLower(t.Driver)
	for _, f := range c {
		if slices.Contains(f.Drivers(), name) {
			return f.Open(ctx, t, password)
		}
	}
	return nil, fmt.Errorf("unknown driver %q (available: %s)", t.Driver, strings.Join(c.Drivers(), ", "))
}

func (c combined) DriverInfos() []domain.DriverInfo {
	var out []domain.DriverInfo
	for _, f := range c {
		if d, ok := f.(DriverDescriber); ok {
			out = append(out, d.DriverInfos()...)
		}
	}
	return out
}

// newRegistry builds the driver registry: built-ins plus whatever dbs describes.
func newRegistry(dbs DatabaseFactory) *domain.Registry {
	r := domain.NewRegistry()
	if d, ok := dbs.(DriverDescriber); ok {
		for _, i := range d.DriverInfos() {
			r.Add(i)
		}
	}
	return r
}

// NormalizeDriver maps a driver name or alias onto its canonical name.
func (s *Service) NormalizeDriver(name string) string { return s.drivers.Normalize(name) }

// DriverInfo describes a driver (aliases accepted).
func (s *Service) DriverInfo(name string) (domain.DriverInfo, bool) { return s.drivers.Info(name) }

// ParseConnectionString parses a connection string for any known driver,
// including plugins.
func (s *Service) ParseConnectionString(driver, conn string) (domain.Target, string, error) {
	return s.drivers.ParseConnectionString(driver, conn)
}

// ---- Driver plugins ----
//
// Plugins are registered per project (.godwit/config.json) or globally for the
// user (~/.godwit/config.json). Both lists are loaded at startup, and a project
// entry replaces a global one with the same name.

// SetGlobalStore enables global plugins, kept in store (the user's ~/.godwit).
func (s *Service) SetGlobalStore(store ProjectStore) error {
	g, err := store.Load()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalStore, s.global = store, g
	return nil
}

// HasGlobal reports whether global plugins are available.
func (s *Service) HasGlobal() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.globalStore != nil
}

// Plugins returns the plugins registered for this project.
func (s *Service) Plugins() []domain.PluginSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.project.Plugins)
}

// GlobalPlugins returns the plugins registered for the user.
func (s *Service) GlobalPlugins() []domain.PluginSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.global.Plugins)
}

// AddPlugin registers a driver plugin for this project and saves the project.
// It takes effect the next time godwit starts.
func (s *Service) AddPlugin(p domain.PluginSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return addPlugin(&s.project, s.store, p)
}

// AddGlobalPlugin registers a driver plugin for the user.
func (s *Service) AddGlobalPlugin(p domain.PluginSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.globalStore == nil {
		return errNoGlobal
	}
	return addPlugin(&s.global, s.globalStore, p)
}

// RemovePlugin unregisters a driver plugin from this project. Targets that use
// its driver are left alone and fail until the plugin is added again.
func (s *Service) RemovePlugin(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return removePlugin(&s.project, s.store, name)
}

// RemoveGlobalPlugin unregisters a driver plugin for the user.
func (s *Service) RemoveGlobalPlugin(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.globalStore == nil {
		return errNoGlobal
	}
	return removePlugin(&s.global, s.globalStore, name)
}

var errNoGlobal = errors.New("global plugins are unavailable: no home directory")

func addPlugin(proj *domain.Project, store ProjectStore, p domain.PluginSpec) error {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	switch {
	case p.Name == "":
		return fmt.Errorf("plugin name is required")
	case strings.TrimSpace(p.Command) == "":
		return fmt.Errorf("plugin command is required")
	}
	if slices.ContainsFunc(proj.Plugins, func(x domain.PluginSpec) bool { return x.Name == p.Name }) {
		return fmt.Errorf("a plugin named %q already exists", p.Name)
	}
	proj.Plugins = append(proj.Plugins, p)
	return store.Save(*proj)
}

func removePlugin(proj *domain.Project, store ProjectStore, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	i := slices.IndexFunc(proj.Plugins, func(x domain.PluginSpec) bool { return x.Name == name })
	if i < 0 {
		return fmt.Errorf("no plugin named %q", name)
	}
	proj.Plugins = slices.Delete(proj.Plugins, i, i+1)
	return store.Save(*proj)
}

// DriverNoHost reports whether a driver has no network endpoint, so targets
// need no host or user.
func (s *Service) DriverNoHost(name string) bool {
	i, ok := s.drivers.Info(name)
	return ok && i.NoHost
}
