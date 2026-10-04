package domain

import (
	"fmt"
	"strings"
)

// PluginSpec declares an external driver plugin: an executable that speaks
// godwit's plugin protocol (see internal/adapters/plugin).
type PluginSpec struct {
	Name    string   // driver name the plugin is expected to provide
	Command string   // executable name or path
	Args    []string // extra arguments
}

// MergePlugins combines the user's global plugins with a project's. A project
// entry replaces a global one with the same name; order is global first.
func MergePlugins(global, project []PluginSpec) []PluginSpec {
	var out []PluginSpec
	for _, g := range global {
		overridden := false
		for _, p := range project {
			overridden = overridden || strings.EqualFold(p.Name, g.Name)
		}
		if !overridden {
			out = append(out, g)
		}
	}
	return append(out, project...)
}

// DriverInfo describes how a driver's connection strings are written. The
// built-in drivers are described by NewRegistry; plugins supply their own.
type DriverInfo struct {
	Name    string
	Aliases []string
	Schemes []string
	// Parse, when set, replaces godwit's URL parsing. It returns the target
	// (Name and Driver are filled in by the registry) and its password, and
	// owns validation: godwit does not require a host, database or user.
	Parse func(conn string) (t Target, password string, err error)
	// NoHost marks drivers without a network endpoint (for example file based
	// databases), so forms do not insist on a host and user.
	NoHost bool

	dsn func(t *Target, conn string) (string, error) // extra non-URL form (built-ins)
}

// Registry knows every driver's names, aliases and connection-string syntax.
type Registry struct {
	infos   map[string]DriverInfo
	aliases map[string]string
}

// NewRegistry returns a Registry holding the built-in drivers.
func NewRegistry() *Registry {
	r := &Registry{infos: map[string]DriverInfo{}, aliases: map[string]string{}}
	r.Add(DriverInfo{Name: "postgres", Aliases: []string{"postgresql", "pg"}, Schemes: []string{"postgres", "postgresql"}})
	r.Add(DriverInfo{Name: "mysql", Aliases: []string{"mariadb"}, Schemes: []string{"mysql", "mariadb"}, dsn: parseMySQLDSN})
	return r
}

// Add registers a driver, replacing any earlier one with the same name.
func (r *Registry) Add(i DriverInfo) {
	i.Name = strings.ToLower(strings.TrimSpace(i.Name))
	r.infos[i.Name] = i
	for _, a := range i.Aliases {
		r.aliases[strings.ToLower(strings.TrimSpace(a))] = i.Name
	}
}

// Normalize maps a driver name or alias onto the driver's canonical name.
func (r *Registry) Normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if c, ok := r.aliases[n]; ok {
		return c
	}
	return n
}

// Info returns the driver named name (aliases accepted).
func (r *Registry) Info(name string) (DriverInfo, bool) {
	i, ok := r.infos[r.Normalize(name)]
	return i, ok
}

// ParseConnectionString turns a connection string into a Target (without a
// name) and its password. The password is returned separately so callers never
// store it in a Target.
func (r *Registry) ParseConnectionString(driver, conn string) (t Target, password string, err error) {
	driver = r.Normalize(driver)
	conn = strings.TrimSpace(conn)
	t.Driver = driver
	info, ok := r.infos[driver]
	if !ok {
		return t, "", fmt.Errorf("unsupported driver %q", driver)
	}

	if info.Parse != nil {
		t, password, err = info.Parse(conn)
		if err != nil {
			return Target{Driver: driver}, "", err
		}
		t.Driver = driver
		return t, password, nil
	}

	switch {
	case strings.Contains(conn, "://"):
		password, err = parseURL(&t, info.Schemes, conn)
	case info.dsn != nil:
		password, err = info.dsn(&t, conn)
	default:
		err = fmt.Errorf("expected a URL like %s://user:pass@host:port/database", info.Schemes[0])
	}
	if err != nil {
		return Target{Driver: driver}, "", err
	}
	switch {
	case t.Host == "":
		return Target{Driver: driver}, "", fmt.Errorf("connection string has no host")
	case t.Database == "":
		return Target{Driver: driver}, "", fmt.Errorf("connection string has no database name")
	case t.User == "":
		return Target{Driver: driver}, "", fmt.Errorf("connection string has no user")
	}
	return t, password, nil
}
