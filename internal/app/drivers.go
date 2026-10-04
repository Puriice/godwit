package app

import (
	"context"
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

// Plugins returns the configured driver plugins.
func (s *Service) Plugins() []domain.PluginSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.project.Plugins)
}

// AddPlugin registers a driver plugin and saves the project. It takes effect
// the next time godwit starts.
func (s *Service) AddPlugin(p domain.PluginSpec) error {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	switch {
	case p.Name == "":
		return fmt.Errorf("plugin name is required")
	case strings.TrimSpace(p.Command) == "":
		return fmt.Errorf("plugin command is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.project.Plugins, func(x domain.PluginSpec) bool { return x.Name == p.Name }) {
		return fmt.Errorf("a plugin named %q already exists", p.Name)
	}
	s.project.Plugins = append(s.project.Plugins, p)
	return s.store.Save(s.project)
}

// RemovePlugin unregisters a driver plugin. Targets that use its driver are
// left alone and fail until the plugin is added again.
func (s *Service) RemovePlugin(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.project.Plugins, func(x domain.PluginSpec) bool { return x.Name == name })
	if i < 0 {
		return fmt.Errorf("no plugin named %q", name)
	}
	s.project.Plugins = slices.Delete(s.project.Plugins, i, i+1)
	return s.store.Save(s.project)
}

// DriverNoHost reports whether a driver has no network endpoint, so targets
// need no host or user.
func (s *Service) DriverNoHost(name string) bool {
	i, ok := s.drivers.Info(name)
	return ok && i.NoHost
}
