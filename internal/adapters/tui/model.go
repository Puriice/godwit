// Package tui is the Bubble Tea driving adapter. It talks only to the app
// service and domain types; SQL, files and drivers never appear here.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	scrFiles
	scrProcesses
	scrPlugins
)

// panels are the top-level screens that left and right cycle through.
var panels = []struct {
	screen screen
	name   string
}{{scrTargets, "targets"}, {scrFiles, "migrations"}, {scrProcesses, "processes"}, {scrPlugins, "plugins"}}

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
		// cancelled is set when the run ended because the user cancelled it.
		cancelled bool
	}
	clearedMsg struct {
		target string
		err    error
	}
	forcedMsg struct {
		target string
		label  string
		to     domain.State
		err    error
	}
	// bgPollMsg carries a background run's log, read while following it.
	bgPollMsg struct {
		target string
		st     app.RunStatus
		err    error
	}
	// bgAttachMsg carries the log found at startup, which may belong to a run
	// that an earlier TUI session started.
	bgAttachMsg struct {
		target string
		st     app.RunStatus
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
	// queue holds the versions the current run is executing or still has
	// ahead of it; they show as running whatever the database says.
	queue map[int64]bool
	redo  bool // the run is a redo: its migration is reverted, then applied

	// What the processes panel shows about the latest run: kept after it
	// ends, until the next one starts.
	job         domain.Job
	pid         int                // the background worker's process id; 0 when unknown
	total, done int                // migrations the run announced, and how many it completed
	order       []int64            // the versions the run announced, in the order it takes them
	finished    map[int64]bool     // the ones it has completed
	cancelling  bool               // a cancel was requested; the run has not stopped yet
	cancelled   bool               // the latest run ended because it was cancelled
	cancel      context.CancelFunc // cancels an in-process run; nil otherwise
	pending     []domain.Job       // jobs queued behind the run, oldest first
	// waitPid is the worker that handed over to the next queued job: the
	// TUI keeps following the target until a different worker shows up.
	waitPid int
	waits   int // polls spent waiting for that
}

// begin resets what is tracked about a run, for one that is starting or has
// just been found already running. pid is 0 when not known yet.
func (st *targetState) begin(job domain.Job, pid int) {
	st.job, st.pid, st.redo = job, pid, job.Op == domain.OpRedo
	st.queue, st.order, st.finished = nil, nil, nil
	st.total, st.done = 0, 0
	st.cancelling, st.cancelled = false, false
}

// markRunning shows the migrations of the current run as running. Without it
// the one being executed reads as dirty, because a block that cannot be rolled
// back is recorded dirty until it finishes.
func (st *targetState) markRunning() {
	if !st.running {
		return
	}
	for i := range st.items {
		if st.queue[st.items[i].Version] {
			st.items[i].State = domain.Running
		}
	}
}

// track updates the queue from a run's queued and finished versions.
// Versions finished stay queued for a redo, which reports its revert and
// its re-apply separately.
func (st *targetState) track(queued, finished []int64, keepFinished bool) {
	if st.queue == nil {
		st.queue = map[int64]bool{}
	}
	for _, v := range queued {
		st.queue[v] = true
	}
	if !keepFinished {
		if st.finished == nil {
			st.finished = map[int64]bool{}
		}
		st.order = append(st.order, queued...)
		st.total += len(queued)
		st.done += len(finished)
		for _, v := range finished {
			delete(st.queue, v)
			st.finished[v] = true
		}
	}
}

// Model is the root Bubble Tea model.
type Model struct {
	svc    *app.Service
	bg     app.BackgroundRunner // nil runs migrations inside this process
	screen screen
	prev   screen // screen to return to when a form closes
	cursor int    // targets list
	mcur   int    // migrations list
	mdebug bool   // the migrations screen shows its debug panel
	fcur   int    // migration files list
	prcur  int    // processes list
	pcur   int    // plugins list
	notice string

	pluginOps PluginOps
	pbusy     string // shown while a plugin install runs

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
	cmds = append(cmds, m.attachRuns())
	return tea.Batch(cmds...)
}

func (m *Model) refresh(t domain.Target) tea.Cmd {
	st := m.state(t.Name)
	if st.running && m.bg == nil { // a background run is read while it works
		return nil
	}
	if t.Disabled { // never connect to a disabled target
		st.loading, st.err = false, nil
		return nil
	}
	if m.svc.NeedsPassword(t.Name) && !m.svc.HasPassword(t.Name) {
		st.err = fmt.Errorf("no password; press e to edit the target and enter it")
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

// run applies (Up) or reverts (Down) n migrations on one target.
func (m *Model) run(t domain.Target, dir domain.Direction, n int) tea.Cmd {
	op := domain.OpUp
	if dir == domain.Down {
		op = domain.OpDown
	}
	return m.startRun(t, domain.Job{Target: t.Name, Op: op, N: n})
}

// upTo applies the pending migrations up to and including version.
func (m *Model) upTo(t domain.Target, version int64) tea.Cmd {
	return m.startRun(t, domain.Job{Target: t.Name, Op: domain.OpUpTo, Version: version})
}

// downTo rolls back everything newer than version.
func (m *Model) downTo(t domain.Target, version int64) tea.Cmd {
	return m.startRun(t, domain.Job{Target: t.Name, Op: domain.OpDownTo, Version: version})
}

// downBatch rolls back the latest apply run.
func (m *Model) downBatch(t domain.Target) tea.Cmd {
	return m.startRun(t, domain.Job{Target: t.Name, Op: domain.OpBatch})
}

// applyOnly applies just one pending migration.
func (m *Model) applyOnly(t domain.Target, version int64) tea.Cmd {
	return m.startRun(t, domain.Job{Target: t.Name, Op: domain.OpOnly, Version: version})
}

// redo reverts one applied migration and applies it again.
func (m *Model) redo(t domain.Target, version int64) tea.Cmd {
	return m.startRun(t, domain.Job{Target: t.Name, Op: domain.OpRedo, Version: version})
}

// startRun performs job, streaming its progress events and a final
// runDoneMsg back as messages. With a background runner the job runs in a
// separate process that keeps going if the TUI exits; otherwise in-process.
// It does nothing for a target that is disabled, already running, or missing
// its password.
func (m *Model) startRun(t domain.Target, job domain.Job) tea.Cmd {
	dir := job.Direction()
	st := m.state(t.Name)
	if t.Disabled {
		return nil
	}
	if st.running {
		return m.enqueue(t, job)
	}
	if m.svc.NeedsPassword(t.Name) && !m.svc.HasPassword(t.Name) {
		st.err = fmt.Errorf("no password; press e to edit the target and enter it")
		return nil
	}
	st.running, st.err, st.runErr = true, nil, nil
	st.begin(job, 0)
	if st.redo {
		st.track([]int64{job.Version}, nil, true)
	}
	st.log = append(st.log, fmt.Sprintf("— %s —", dir))
	if m.bg != nil {
		if _, err := m.bg.Start(job); err != nil {
			st.running, st.runErr = false, err
			st.addLog("✗ " + err.Error())
			return nil
		}
		return pollRun(m.bg, t.Name, 0)
	}
	ch := make(chan tea.Msg, 64)
	st.events = ch

	svc := m.svc
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	go func() {
		defer close(ch)
		defer cancel()
		prog := func(e domain.Event) { ch <- progressMsg{target: t.Name, ev: e} }
		n, err := svc.RunJob(ctx, job, prog)
		ch <- runDoneMsg{target: t.Name, dir: dir, n: n, err: err, cancelled: ctx.Err() != nil && err != nil}
	}()
	return waitMsg(ch)
}

// enqueue queues job behind the run the target already has. The run starts it
// when it ends well; a failed or cancelled run drops it.
func (m *Model) enqueue(t domain.Target, job domain.Job) tea.Cmd {
	st := m.state(t.Name)
	if m.bg == nil {
		m.notice = "a run is in progress on " + t.Name
		return nil
	}
	queued, err := m.bg.Start(job)
	switch {
	case err != nil:
		m.notice = "queueing: " + err.Error()
	case queued:
		st.pending = append(st.pending, job)
		st.addLog(fmt.Sprintf("＋ queued %s", jobLabel(job)))
		m.notice = fmt.Sprintf("queued %s behind the run on %s", jobLabel(job), t.Name)
	default:
		// The run ended just before the job was handed over, so the job started
		// on its own. Follow it as the worker that handed over would have.
		st.waitPid, st.waits = st.pid, 0
		st.addLog(fmt.Sprintf("＋ started %s", jobLabel(job)))
	}
	return nil
}

// jobLabel is a short description of a job: "up -n 2", "redo 7".
func jobLabel(j domain.Job) string {
	switch {
	case j.Version != 0:
		return fmt.Sprintf("%s %d", j.Op, j.Version)
	case j.N != 0:
		return fmt.Sprintf("%s -n %d", j.Op, j.N)
	}
	return string(j.Op)
}

// maxHandoverPolls bounds the polls (one per pollInterval) spent waiting for
// the worker that takes over after a run, which waits a moment before
// starting.
const maxHandoverPolls = 50

// trackJob feeds a background run's announcements into the queue. A redo is
// not queued by the run, so its one migration is queued here.
func (st *targetState) trackJob(rs app.RunStatus) {
	if st.redo {
		rs.Queued = append(rs.Queued, rs.Job.Version)
	}
	st.track(rs.Queued, rs.Finished, st.redo)
}

// pollInterval is how often a background run's log is re-read.
const pollInterval = 300 * time.Millisecond

// pollRun waits, then reads target's background log from offset from.
func pollRun(bg app.BackgroundRunner, target string, from int64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(pollInterval)
		st, err := bg.Poll(target, from)
		return bgPollMsg{target: target, st: st, err: err}
	}
}

// attachRuns finds background runs that outlived an earlier TUI session and
// picks up following them.
func (m *Model) attachRuns() tea.Cmd {
	if m.bg == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, t := range m.targets() {
		target := t.Name
		cmds = append(cmds, func() tea.Msg {
			st, err := m.bg.Poll(target, 0)
			return bgAttachMsg{target: target, st: st, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// WithBackground makes runs happen in detached worker processes, so they keep
// going after the TUI exits and are picked up again the next time it opens.
func (m *Model) WithBackground(bg app.BackgroundRunner) *Model {
	m.bg = bg
	return m
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
			st.markRunning()
		}
		return m, nil

	case progressMsg:
		st := m.state(msg.target)
		switch msg.ev.Phase {
		case domain.Queued:
			st.track([]int64{msg.ev.Version}, nil, false)
			st.markRunning()
			return m, waitMsg(st.events)
		case domain.Done:
			st.track(nil, []int64{msg.ev.Version}, st.redo)
		}
		st.addLog(formatEvent(msg.ev))
		return m, waitMsg(st.events)

	case bgAttachMsg:
		st := m.state(msg.target)
		if st.running || msg.err != nil || !msg.st.Active {
			return m, nil
		}
		st.running, st.err, st.runErr = true, nil, nil
		st.begin(msg.st.Job, msg.st.Pid)
		st.pending = msg.st.Pending
		st.addLog(fmt.Sprintf("— %s (still running in the background) —", msg.st.Job.Direction()))
		for _, l := range msg.st.Lines {
			st.addLog(l)
		}
		st.trackJob(msg.st)
		st.markRunning()
		// The status read before the run was noticed shows it as dirty.
		cmds := []tea.Cmd{pollRun(m.bg, msg.target, msg.st.Next)}
		if t, ok := m.svc.Target(msg.target); ok {
			cmds = append(cmds, m.refresh(t))
		}
		return m, tea.Batch(cmds...)

	case bgPollMsg:
		st := m.state(msg.target)
		if !st.running {
			return m, nil
		}
		done := func(err error) (tea.Model, tea.Cmd) {
			return m.Update(runDoneMsg{target: msg.target, dir: msg.st.Job.Direction(), n: msg.st.Count, err: err, cancelled: msg.st.Cancelled})
		}
		if msg.err != nil {
			return done(msg.err)
		}
		if st.waitPid != 0 {
			// The run ended and handed over: the log is the old worker's until
			// a different worker has started the next job.
			if msg.st.Job.Op == "" || msg.st.Pid == st.waitPid {
				if st.waits++; st.waits > maxHandoverPolls {
					return done(errors.New("the queued run did not start; check the target's state"))
				}
				return m, pollRun(m.bg, msg.target, 0)
			}
			st.waitPid, st.waits = 0, 0
			st.begin(msg.st.Job, msg.st.Pid)
			st.addLog(fmt.Sprintf("— %s (from the queue) —", msg.st.Job.Direction()))
		}
		for _, l := range msg.st.Lines {
			st.addLog(l)
		}
		st.pending = msg.st.Pending
		st.pid, st.cancelling = msg.st.Pid, msg.st.Cancelling
		st.trackJob(msg.st)
		st.markRunning()
		var refresh tea.Cmd
		if len(msg.st.Finished) > 0 && !msg.st.Done {
			if t, ok := m.svc.Target(msg.target); ok {
				refresh = m.refresh(t) // show what has been applied so far
			}
		}
		switch {
		case msg.st.Done && msg.st.More && msg.st.Err == "":
			// Ended well and hands over to the next queued job: show this run
			// as finished and keep following the target.
			st.addLog(fmt.Sprintf("✓ %d migration(s) %s", msg.st.Count, doneWord(msg.st.Job.Direction())))
			st.waitPid, st.waits = msg.st.Pid, 0
			if t, ok := m.svc.Target(msg.target); ok {
				refresh = m.refresh(t)
			}
			return m, tea.Batch(refresh, pollRun(m.bg, msg.target, 0))
		case msg.st.Done && msg.st.Err != "":
			return done(errors.New(msg.st.Err))
		case msg.st.Done:
			return done(nil)
		case !msg.st.Active:
			return done(errors.New("background run stopped without finishing; check the target's state"))
		}
		return m, tea.Batch(refresh, pollRun(m.bg, msg.target, msg.st.Next))

	case pluginDoneMsg:
		m.pbusy = ""
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = msg.notice
		}
		return m, nil

	case clearedMsg:
		if msg.err != nil {
			m.notice = "clearing dirty flag: " + msg.err.Error()
			return m, nil
		}
		if t, ok := m.svc.Target(msg.target); ok {
			return m, m.refresh(t)
		}
		return m, nil

	case forcedMsg:
		st := m.state(msg.target)
		if msg.err != nil {
			st.addLog("✗ debug: " + msg.err.Error())
			m.notice = "forcing state: " + msg.err.Error()
			return m, nil
		}
		st.addLog(fmt.Sprintf("• debug: forced %s to %s", msg.label, msg.to))
		if t, ok := m.svc.Target(msg.target); ok {
			return m, m.refresh(t)
		}
		return m, nil

	case runDoneMsg:
		st := m.state(msg.target)
		st.running, st.events, st.queue, st.cancel = false, nil, nil, nil
		st.pending, st.waitPid, st.waits = nil, 0, 0
		st.cancelled = msg.cancelled || (st.cancelling && msg.err != nil)
		st.cancelling = false
		if msg.err != nil {
			st.runErr = msg.err
			if st.cancelled {
				st.addLog("■ cancelled: " + msg.err.Error())
			} else {
				st.addLog("✗ " + msg.err.Error())
			}
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
		case scrFiles:
			return m.updateFiles(key)
		case scrProcesses:
			return m.updateProcesses(key)
		case scrPlugins:
			return m.updatePlugins(key)
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
	case scrFiles:
		return m.viewFiles()
	case scrProcesses:
		return m.viewProcesses()
	case scrPlugins:
		return m.viewPlugins()
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
	switch d {
	case domain.Up:
		return "applied"
	case domain.Redo:
		return "redone"
	}
	return "reverted"
}

// switchPanel moves delta panels along the top-level panels, wrapping around.
func (m *Model) switchPanel(delta int) {
	for i, p := range panels {
		if p.screen == m.screen {
			m.screen = panels[(i+delta+len(panels))%len(panels)].screen
			return
		}
	}
}

// tabs renders the panel names with the current one highlighted.
func (m *Model) tabs() string {
	var parts []string
	for _, p := range panels {
		if p.screen == m.screen {
			parts = append(parts, cursorStyle.Render("["+p.name+"]"))
		} else {
			parts = append(parts, p.name)
		}
	}
	return strings.Join(parts, " ")
}
