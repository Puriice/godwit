package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

const (
	handshakeTimeout = 10 * time.Second
	parseTimeout     = 15 * time.Second
	closeTimeout     = 3 * time.Second
	unlockTimeout    = 30 * time.Second
)

type driver struct {
	spec domain.PluginSpec
	path string
	hs   handshakeResult
}

// Factory implements app.DatabaseFactory (and app.DriverDescriber) for the
// drivers provided by plugins.
type Factory struct {
	root     string
	drivers  map[string]*driver
	warnings []string
}

var (
	_ app.DatabaseFactory = (*Factory)(nil)
	_ app.DriverDescriber = (*Factory)(nil)
)

// New starts every plugin once to learn what driver it provides. A plugin that
// cannot be started or speaks another protocol version is skipped and reported
// by Warnings, so a broken plugin never takes the built-in drivers down.
func New(root string, specs []domain.PluginSpec) *Factory {
	f := &Factory{root: root, drivers: map[string]*driver{}}
	builtin := domain.NewRegistry()
	for _, spec := range specs {
		d, err := probe(root, spec)
		if err != nil {
			f.warnings = append(f.warnings, err.Error())
			continue
		}
		name := strings.ToLower(d.hs.Driver)
		if _, ok := builtin.Info(name); ok {
			f.warnings = append(f.warnings, fmt.Sprintf("plugin %s: driver %q is built in and cannot be replaced", spec.Name, name))
			continue
		}
		if _, dup := f.drivers[name]; dup {
			f.warnings = append(f.warnings, fmt.Sprintf("plugin %s: driver %q is already provided by another plugin", spec.Name, name))
			continue
		}
		f.drivers[name] = d
	}
	return f
}

// Warnings lists plugins that could not be loaded.
func (f *Factory) Warnings() []string { return f.warnings }

// Probe starts a plugin, checks its handshake and describes its driver. It is
// what "godwit plugin add" uses to validate a plugin before saving it.
func Probe(root string, spec domain.PluginSpec) (domain.DriverInfo, error) {
	d, err := probe(root, spec)
	if err != nil {
		return domain.DriverInfo{}, err
	}
	return d.info(root), nil
}

func probe(root string, spec domain.PluginSpec) (*driver, error) {
	path, err := resolve(root, spec.Command)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: cannot find %q: %w", spec.Name, spec.Command, err)
	}
	c, err := start(spec.Name, path, spec.Args, root)
	if err != nil {
		return nil, err
	}
	defer c.close()

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	var hs handshakeResult
	if err := c.call(ctx, "handshake", map[string]int{"protocol": ProtocolVersion}, &hs); err != nil {
		return nil, err
	}
	switch {
	case hs.Protocol != ProtocolVersion:
		return nil, fmt.Errorf("plugin %s speaks protocol %d; this godwit speaks %d", spec.Name, hs.Protocol, ProtocolVersion)
	case strings.TrimSpace(hs.Driver) == "":
		return nil, fmt.Errorf("plugin %s did not name its driver", spec.Name)
	}
	return &driver{spec: spec, path: path, hs: hs}, nil
}

func (d *driver) info(root string) domain.DriverInfo {
	return domain.DriverInfo{
		Name:    d.hs.Driver,
		Aliases: d.hs.Aliases,
		Schemes: d.hs.Schemes,
		NoHost:  d.hs.NoHost,
		Parse: func(conn string) (domain.Target, string, error) {
			c, err := start(d.spec.Name, d.path, d.spec.Args, root)
			if err != nil {
				return domain.Target{}, "", err
			}
			defer c.close()
			ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
			defer cancel()
			var res parseResult
			if err := c.call(ctx, "parse_connection", parseParams{ConnString: conn}, &res); err != nil {
				return domain.Target{}, "", err
			}
			return fromWire(res.Target), res.Password, nil
		},
	}
}

// Drivers lists the driver names the loaded plugins provide, sorted.
func (f *Factory) Drivers() []string {
	names := make([]string, 0, len(f.drivers))
	for n := range f.drivers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DriverInfos describes the plugin drivers' connection-string syntax.
func (f *Factory) DriverInfos() []domain.DriverInfo {
	var out []domain.DriverInfo
	for _, n := range f.Drivers() {
		out = append(out, f.drivers[n].info(f.root))
	}
	return out
}

// Open starts a dedicated plugin process for the target and connects it.
func (f *Factory) Open(ctx context.Context, t domain.Target, password string) (app.Database, error) {
	name := strings.ToLower(t.Driver)
	d, ok := f.drivers[name]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q (available: %s)", t.Driver, strings.Join(f.Drivers(), ", "))
	}
	c, err := start(d.spec.Name, d.path, d.spec.Args, f.root)
	if err != nil {
		return nil, err
	}
	if err := c.call(ctx, "open", openParams{Target: toWire(t), Password: password}, nil); err != nil {
		c.close()
		return nil, err
	}
	return &database{c: c, driver: name}, nil
}

func toWire(t domain.Target) wireTarget {
	return wireTarget{Name: t.Name, Driver: t.Driver, Host: t.Host, Port: t.Port,
		Database: t.Database, User: t.User, Params: t.Params}
}

func fromWire(w wireTarget) domain.Target {
	return domain.Target{Name: w.Name, Driver: w.Driver, Host: w.Host, Port: w.Port,
		Database: w.Database, User: w.User, Params: w.Params}
}
