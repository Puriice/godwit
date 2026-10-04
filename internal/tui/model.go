// Package tui is the Bubble Tea front end. It talks only to config, migration
// and runner; SQL never appears here.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/migration"
	"github.com/puriice/godwit/internal/runner"
)

type screen int

const (
	scrTargets screen = iota
	scrMigrations
	scrForm
)

const maxLog = 200

// Messages produced by commands.
type (
	statusMsg struct {
		target string
		items  []runner.Item
		err    error
	}
	progressMsg struct {
		target string
		ev     runner.Event
	}
	runDoneMsg struct {
		target string
		dir    runner.Direction
		n      int
		err    error
	}
	clearedMsg struct {
		target string
		err    error
	}
)

// targetState is everything the UI knows about one target at runtime.
type targetState struct {
	items   []runner.Item
	loaded  bool
	loading bool
	running bool
	err     error // last status-load error
	runErr  error // last run error; survives the status refresh after a run
	log     []string
	events  chan tea.Msg
}

// Model is the root Bubble Tea model.
type Model struct {
	proj   *config.Project
	screen screen
	prev   screen // screen to return to when a form closes
	cursor int    // targets list
	mcur   int    // migrations list
	notice string

	migs   []*migration.Migration
	migErr error
	states map[string]*targetState

	form       *huh.Form
	formTitle  string
	onFormDone func(m *Model) tea.Cmd
	// credQueue holds targets still missing a password at startup.
	credQueue []config.Target

	width, height int
}

// New creates the model for a loaded project.
func New(p *config.Project) *Model {
	m := &Model{proj: p, states: map[string]*targetState{}, width: 80, height: 24}
	m.reloadMigrations()
	for _, t := range p.Targets {
		m.states[t.Name] = &targetState{}
	}
	m.credQueue = p.MissingPasswords()
	return m
}

func (m *Model) reloadMigrations() {
	m.migs, m.migErr = migration.Load(m.proj.MigrationsPath())
}

func (m *Model) state(name string) *targetState {
	st, ok := m.states[name]
	if !ok {
		st = &targetState{}
		m.states[name] = st
	}
	return st
}

func (m *Model) targets() []config.Target { return m.proj.Targets }

func (m *Model) currentTarget() (config.Target, bool) {
	ts := m.targets()
	if m.cursor < 0 || m.cursor >= len(ts) {
		return config.Target{}, false
	}
	return ts[m.cursor], true
}

// Init prompts for missing credentials, then loads every target's status.
func (m *Model) Init() tea.Cmd {
	if cmd := m.nextCredentialForm(); cmd != nil {
		return cmd
	}
	return m.refreshAll()
}

// nextCredentialForm opens the form for the next target missing a password.
// It returns nil when the queue is empty.
func (m *Model) nextCredentialForm() tea.Cmd {
	if len(m.credQueue) == 0 {
		return nil
	}
	t := m.credQueue[0]
	m.credQueue = m.credQueue[1:]
	return m.openCredentialForm(t)
}

func (m *Model) refreshAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.targets() {
		cmds = append(cmds, m.refresh(t))
	}
	return tea.Batch(cmds...)
}

func (m *Model) refresh(t config.Target) tea.Cmd {
	st := m.state(t.Name)
	if st.running {
		return nil
	}
	if _, ok := m.proj.Password(t); !ok {
		st.err = fmt.Errorf("no password; press p to enter it")
		st.loaded = false
		return nil
	}
	st.loading, st.err = true, nil
	proj, migs := m.proj, m.migs
	return func() tea.Msg {
		ctx := context.Background()
		c, err := runner.Open(ctx, proj, t)
		if err != nil {
			return statusMsg{target: t.Name, err: err}
		}
		defer c.Close()
		items, err := runner.Status(ctx, c, migs)
		return statusMsg{target: t.Name, items: items, err: err}
	}
}

// run applies or reverts migrations on one target, streaming progress.
func (m *Model) run(t config.Target, dir runner.Direction, n int) tea.Cmd {
	st := m.state(t.Name)
	if st.running {
		return nil
	}
	if _, ok := m.proj.Password(t); !ok {
		st.err = fmt.Errorf("no password; press p to enter it")
		return nil
	}
	st.running, st.err, st.runErr = true, nil, nil
	st.log = append(st.log, fmt.Sprintf("— %s —", dir))
	ch := make(chan tea.Msg, 64)
	st.events = ch
	proj, migs := m.proj, m.migs

	go func() {
		defer close(ch)
		ctx := context.Background()
		c, err := runner.Open(ctx, proj, t)
		if err != nil {
			ch <- runDoneMsg{target: t.Name, dir: dir, err: err}
			return
		}
		defer c.Close()
		prog := func(e runner.Event) { ch <- progressMsg{target: t.Name, ev: e} }
		var cnt int
		if dir == runner.Up {
			cnt, err = runner.MigrateUp(ctx, c, migs, n, prog)
		} else {
			cnt, err = runner.MigrateDown(ctx, c, migs, n, prog)
		}
		ch <- runDoneMsg{target: t.Name, dir: dir, n: cnt, err: err}
	}()
	return waitMsg(ch)
}

func waitMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (st *targetState) addLog(s string) {
	st.log = append(st.log, s)
	if len(st.log) > maxLog {
		st.log = st.log[len(st.log)-maxLog:]
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.form != nil {
			m.form = m.form.WithWidth(formWidth(m.width))
		}
		return m, nil

	case statusMsg:
		st := m.state(msg.target)
		st.loading = false
		st.err = msg.err
		if msg.err == nil {
			st.items, st.loaded = msg.items, true
		}
		return m, nil

	case progressMsg:
		st := m.state(msg.target)
		st.addLog(formatEvent(msg.ev))
		return m, waitMsg(st.events)

	case clearedMsg:
		if msg.err != nil {
			m.notice = "clearing dirty flag: " + msg.err.Error()
			return m, nil
		}
		if t, ok := m.proj.Target(msg.target); ok {
			return m, m.refresh(t)
		}
		return m, nil

	case runDoneMsg:
		st := m.state(msg.target)
		st.running, st.events = false, nil
		if msg.err != nil {
			st.runErr = msg.err
			st.addLog("✗ " + msg.err.Error())
		} else {
			st.addLog(fmt.Sprintf("✓ %d migration(s) %s", msg.n, doneWord(msg.dir)))
		}
		if t, ok := m.proj.Target(msg.target); ok {
			return m, m.refresh(t)
		}
		return m, nil
	}

	if m.screen == scrForm {
		return m.updateForm(msg)
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "ctrl+c" {
			return m, tea.Quit
		}
		m.notice = ""
		switch m.screen {
		case scrTargets:
			return m.updateTargets(key)
		case scrMigrations:
			return m.updateMigrations(key)
		}
	}
	return m, nil
}

func (m *Model) View() string {
	switch m.screen {
	case scrForm:
		return m.viewForm()
	case scrMigrations:
		return m.viewMigrations()
	default:
		return m.viewTargets()
	}
}

func formatEvent(e runner.Event) string {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
	case runner.Started:
		return fmt.Sprintf("… %s %s", e.Direction, label)
	case runner.Done:
		return fmt.Sprintf("✓ %s %s", e.Direction, label)
	default:
		return fmt.Sprintf("✗ %s %s: %v", e.Direction, label, e.Err)
	}
}

func doneWord(d runner.Direction) string {
	if d == runner.Up {
		return "applied"
	}
	return "reverted"
}
