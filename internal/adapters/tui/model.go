// Package tui is the Bubble Tea driving adapter. It talks only to the app
// service and domain types; SQL, files and drivers never appear here.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
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
		items  []domain.Item
		err    error
	}
	progressMsg struct {
		target string
		ev     domain.Event
	}
	runDoneMsg struct {
		target string
		dir    domain.Direction
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
	items   []domain.Item
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
	svc    *app.Service
	screen screen
	prev   screen // screen to return to when a form closes
	cursor int    // targets list
	mcur   int    // migrations list
	notice string

	migCount int // migration definitions found, for display
	migErr   error
	states   map[string]*targetState

	form       *huh.Form
	formTitle  string
	onFormDone func(m *Model) tea.Cmd
	// credQueue holds targets still missing a password at startup.
	credQueue []domain.Target

	width, height int
}

// New creates the model on top of the application service.
func New(svc *app.Service) *Model {
	m := &Model{svc: svc, states: map[string]*targetState{}, width: 80, height: 24}
	m.reloadMigrations()
	for _, t := range svc.Targets() {
		m.states[t.Name] = &targetState{}
	}
	m.credQueue = svc.TargetsMissingPassword()
	return m
}

func (m *Model) reloadMigrations() {
	migs, err := m.svc.Migrations()
	m.migCount, m.migErr = len(migs), err
}

func (m *Model) state(name string) *targetState {
	st, ok := m.states[name]
	if !ok {
		st = &targetState{}
		m.states[name] = st
	}
	return st
}

func (m *Model) targets() []domain.Target { return m.svc.Targets() }

func (m *Model) currentTarget() (domain.Target, bool) {
	ts := m.targets()
	if m.cursor < 0 || m.cursor >= len(ts) {
		return domain.Target{}, false
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

func (m *Model) refresh(t domain.Target) tea.Cmd {
	st := m.state(t.Name)
	if st.running {
		return nil
	}
	if t.Disabled { // never connect to a disabled target
		st.loading, st.err = false, nil
		return nil
	}
	if !m.svc.HasPassword(t.Name) {
		st.err = fmt.Errorf("no password; press p to enter it")
		st.loaded = false
		return nil
	}
	st.loading, st.err = true, nil
	svc := m.svc
	return func() tea.Msg {
		items, err := svc.Status(context.Background(), t.Name)
		return statusMsg{target: t.Name, items: items, err: err}
	}
}

// run applies or reverts migrations on one target, streaming progress.
// toggleTarget enables a disabled target or disables an enabled one.
func (m *Model) toggleTarget(t domain.Target) tea.Cmd {
	st := m.state(t.Name)
	if st.running {
		m.notice = "a run is in progress on " + t.Name
		return nil
	}
	enable := t.Disabled
	if err := m.svc.SetTargetEnabled(t.Name, enable); err != nil {
		m.notice = err.Error()
		return nil
	}
	if !enable {
		st.items, st.loaded, st.loading, st.err, st.runErr = nil, false, false, nil, nil
		m.notice = fmt.Sprintf("disabled %s; press t to enable it again", t.Name)
		return nil
	}
	m.notice = "enabled " + t.Name
	if updated, ok := m.svc.Target(t.Name); ok {
		return m.refresh(updated)
	}
	return nil
}

func (m *Model) run(t domain.Target, dir domain.Direction, n int) tea.Cmd {
	st := m.state(t.Name)
	if st.running || t.Disabled {
		return nil
	}
	if !m.svc.HasPassword(t.Name) {
		st.err = fmt.Errorf("no password; press p to enter it")
		return nil
	}
	st.running, st.err, st.runErr = true, nil, nil
	st.log = append(st.log, fmt.Sprintf("— %s —", dir))
	ch := make(chan tea.Msg, 64)
	st.events = ch
	svc := m.svc

	go func() {
		defer close(ch)
		ctx := context.Background()
		prog := func(e domain.Event) { ch <- progressMsg{target: t.Name, ev: e} }
		var (
			cnt int
			err error
		)
		if dir == domain.Up {
			cnt, err = svc.Up(ctx, t.Name, n, prog)
		} else {
			cnt, err = svc.Down(ctx, t.Name, n, prog)
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
		m.fitForm()
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
		if t, ok := m.svc.Target(msg.target); ok {
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
		if t, ok := m.svc.Target(msg.target); ok {
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

func formatEvent(e domain.Event) string {
	label := fmt.Sprintf("%d_%s", e.Version, e.Name)
	switch e.Phase {
	case domain.Started:
		return fmt.Sprintf("… %s %s", e.Direction, label)
	case domain.Done:
		return fmt.Sprintf("✓ %s %s", e.Direction, label)
	default:
		return fmt.Sprintf("✗ %s %s: %v", e.Direction, label, e.Err)
	}
}

func doneWord(d domain.Direction) string {
	if d == domain.Up {
		return "applied"
	}
	return "reverted"
}
