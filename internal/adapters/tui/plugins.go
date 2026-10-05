package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/domain"
)

// PluginOps are the things the plugins panel needs from the outside world.
// Both are optional: without Probe a plugin is saved unchecked, and without
// Install the panel offers no install.
type PluginOps struct {
	// Probe starts a plugin to check it and describes its driver.
	Probe func(domain.PluginSpec) (domain.DriverInfo, error)
	// Install builds a plugin from a Go package path and returns the command
	// that runs it. global selects ~/.godwit/plugins over .godwit/plugins.
	Install func(ctx context.Context, global bool, pkg string, out io.Writer) (command string, err error)
}

// WithPluginOps enables probing and installing in the plugins panel.
func (m *Model) WithPluginOps(ops PluginOps) *Model {
	m.pluginOps = ops
	return m
}

const pluginsHelp = "←/→ switch panel · ↑/↓ select · a add · i install · x remove · q quit"

// Where a plugin is registered.
const (
	scopeProject = "project"
	scopeGlobal  = "global"
	scopeBoth    = "both"
)

// pluginRow is one line of the plugins panel.
type pluginRow struct {
	spec       domain.PluginSpec
	global     bool
	overridden bool // a global plugin that a project plugin of the same name replaces
}

type pluginDoneMsg struct {
	notice string
	err    error
}

func (m *Model) pluginRows() []pluginRow {
	var rows []pluginRow
	local := m.svc.Plugins()
	for _, p := range local {
		rows = append(rows, pluginRow{spec: p})
	}
	for _, p := range m.svc.GlobalPlugins() {
		over := slices.ContainsFunc(local, func(l domain.PluginSpec) bool { return strings.EqualFold(l.Name, p.Name) })
		rows = append(rows, pluginRow{spec: p, global: true, overridden: over})
	}
	return rows
}

func (m *Model) viewPlugins() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	rows := m.pluginRows()
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("No plugins. Press a to add one"))
		if m.pluginOps.Install != nil {
			b.WriteString(dimStyle.Render(" or i to install one"))
		}
		b.WriteString(dimStyle.Render("."))
		b.WriteRune('\n')
	}
	for i, r := range rows {
		where := "project"
		if r.global {
			where = "global"
		}
		cmd := strings.TrimSpace(r.spec.Command + " " + strings.Join(r.spec.Args, " "))
		label := fmt.Sprintf("%-16s %-8s", r.spec.Name, where)
		if r.overridden {
			label = dimStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s%s %s\n", pointer(i == m.pcur), label, dimStyle.Render(truncate(cmd, max(m.width-30, 10))))
		if r.overridden {
			fmt.Fprintf(&b, "    %s\n", warnStyle.Render("overridden by project"))
		}
	}
	if m.pbusy != "" {
		b.WriteRune('\n')
		b.WriteString(warnStyle.Render(m.pbusy))
		b.WriteRune('\n')
	}
	fmt.Fprintf(&b, "\n%s\n", dimStyle.Render("Plugins load at startup: restart godwit after changing them."))
	help := pluginsHelp
	if m.pluginOps.Install == nil {
		help = strings.Replace(help, " · i install", "", 1)
	}
	return b.String() + m.footer(help)
}

func (m *Model) updatePlugins(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.pluginRows()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.switchPanel(-1)
	case "right", "l":
		m.switchPanel(1)
	case "up", "k":
		if m.pcur > 0 {
			m.pcur--
		}
	case "down", "j":
		if m.pcur < len(rows)-1 {
			m.pcur++
		}
	case "a":
		return m, m.openPluginAddForm()
	case "i":
		if m.pluginOps.Install == nil {
			m.notice = "installing is unavailable here; use: godwit plugin install"
			break
		}
		return m, m.openPluginInstallForm()
	case "x":
		if m.pcur < len(rows) {
			r := rows[m.pcur]
			where := "this project"
			if r.global {
				where = "your global plugins (~/.godwit)"
			}
			return m, m.openConfirm(fmt.Sprintf("Remove plugin %q from %s? Targets using its driver stop working until it is added again.", r.spec.Name, where), func(m *Model) tea.Cmd {
				var err error
				if r.global {
					err = m.svc.RemoveGlobalPlugin(r.spec.Name)
				} else {
					err = m.svc.RemovePlugin(r.spec.Name)
				}
				if err != nil {
					m.notice = err.Error()
				} else {
					m.notice = fmt.Sprintf("removed plugin %q; restart godwit to unload it", r.spec.Name)
				}
				m.pcur = max(min(m.pcur, len(m.pluginRows())-1), 0)
				return nil
			})
		}
	}
	return m, nil
}

// pluginScopeOptions lists where a plugin can go; global needs a home directory.
func (m *Model) pluginScopeOptions() []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("This project", scopeProject)}
	if m.svc.HasGlobal() {
		opts = append(opts,
			huh.NewOption("Global (~/.godwit)", scopeGlobal),
			huh.NewOption("Global and this project", scopeBoth))
	}
	return opts
}

func (m *Model) openPluginAddForm() tea.Cmd {
	var command, name, args string
	scope := scopeProject
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Command").Description("path, or a name in .godwit/plugins, ~/.godwit/plugins or PATH").Value(&command).Validate(required),
		huh.NewInput().Title("Name").Description("blank = the driver name the plugin reports").Value(&name),
		huh.NewInput().Title("Arguments").Description("passed to the plugin when it starts").Value(&args),
		huh.NewSelect[string]().Title("Register").Options(m.pluginScopeOptions()...).Value(&scope),
	))
	return m.openForm("Add plugin", f, func(m *Model) tea.Cmd {
		spec := domain.PluginSpec{Name: strings.TrimSpace(name), Command: strings.TrimSpace(command), Args: strings.Fields(args)}
		m.notice = ""
		msg, err := m.registerPlugin(spec, scope)
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		m.notice = msg
		return nil
	})
}

func (m *Model) openPluginInstallForm() tea.Cmd {
	var pkg, name string
	scope := scopeProject
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Go package").Description("for example github.com/you/godwit-driver-x[@version]. Builds and runs code from the network.").Value(&pkg).Validate(required),
		huh.NewInput().Title("Name").Description("blank = the driver name the plugin reports").Value(&name),
		huh.NewSelect[string]().Title("Register").Options(m.pluginScopeOptions()...).Value(&scope),
	))
	return m.openForm("Install plugin", f, func(m *Model) tea.Cmd {
		pkg, name := strings.TrimSpace(pkg), strings.TrimSpace(name)
		m.pbusy = "installing " + pkg + "…"
		return func() tea.Msg {
			command, err := m.pluginOps.Install(context.Background(), scope != scopeProject, pkg, io.Discard)
			if err != nil {
				return pluginDoneMsg{err: fmt.Errorf("install %s: %w", pkg, err)}
			}
			msg, err := m.registerPlugin(domain.PluginSpec{Name: name, Command: command}, scope)
			return pluginDoneMsg{notice: msg, err: err}
		}
	})
}

// registerPlugin probes a plugin when it can and saves it where scope says.
// Registering the same plugin again is a no-op, so an install can update one.
func (m *Model) registerPlugin(spec domain.PluginSpec, scope string) (string, error) {
	explicit := spec.Name != ""
	if !explicit {
		spec.Name = spec.Command // only used in messages until the plugin names itself
	}
	driver := strings.ToLower(spec.Name)
	if m.pluginOps.Probe != nil {
		info, err := m.pluginOps.Probe(spec)
		if err != nil {
			return "", err
		}
		driver = strings.ToLower(strings.TrimSpace(info.Name))
		if _, builtin := domain.NewRegistry().Info(driver); builtin {
			return "", fmt.Errorf("plugin provides driver %q, which is built in and cannot be replaced", driver)
		}
		if !explicit {
			spec.Name = driver
		}
	} else if !explicit {
		return "", errors.New("a name is required here: the plugin cannot be probed to learn its driver name")
	}
	spec.Name = strings.ToLower(spec.Name)

	type dest struct {
		spec     domain.PluginSpec
		existing []domain.PluginSpec
		add      func(domain.PluginSpec) error
		label    string
	}
	var dests []dest
	if scope != scopeProject {
		g := spec
		// A relative path would mean something different in every project.
		if strings.ContainsAny(g.Command, `/\`) && !filepath.IsAbs(g.Command) {
			abs, err := filepath.Abs(filepath.FromSlash(g.Command))
			if err != nil {
				return "", err
			}
			g.Command = abs
		}
		dests = append(dests, dest{g, m.svc.GlobalPlugins(), m.svc.AddGlobalPlugin, "globally"})
	}
	if scope != scopeGlobal {
		dests = append(dests, dest{spec, m.svc.Plugins(), m.svc.AddPlugin, "in this project"})
	}

	same := func(a, b domain.PluginSpec) bool {
		return strings.EqualFold(a.Name, b.Name) && a.Command == b.Command && slices.Equal(a.Args, b.Args)
	}
	// Check every destination before changing any, so "both" is all or nothing.
	for _, d := range dests {
		for _, p := range d.existing {
			if strings.EqualFold(p.Name, d.spec.Name) && !same(p, d.spec) {
				return "", fmt.Errorf("a plugin named %q already exists %s with a different command (%s); remove it first", p.Name, d.label, p.Command)
			}
		}
	}
	added := false
	for _, d := range dests {
		if slices.ContainsFunc(d.existing, func(p domain.PluginSpec) bool { return same(p, d.spec) }) {
			continue
		}
		if err := d.add(d.spec); err != nil {
			return "", err
		}
		added = true
	}
	if !added {
		return fmt.Sprintf("plugin %q is already registered", spec.Name), nil
	}
	return fmt.Sprintf("added plugin %q (driver %s); restart godwit to use it", spec.Name, driver), nil
}
