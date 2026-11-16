package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/puriice/godwit/internal/domain"
)

const defaultMigrationsDir = "migrations"

// ErrTargetDisabled is returned when an operation needs a disabled target.
var ErrTargetDisabled = errors.New("target is disabled")

// Progress receives run events. It may be nil.
type Progress func(domain.Event)

func (p Progress) emit(e domain.Event) {
	if p != nil {
		p(e)
	}
}

// scope is one .godwit configuration: a plain project, the root of a
// workspace (whose targets are global) or one service of a workspace.
type scope struct {
	name    string // service name; "" for a plain project or a workspace root
	dir     string // service directory relative to the workspace root ("/" separated); "" for the root
	store   ProjectStore
	project domain.Project
}

// table is the state table this scope's migrations are recorded in.
func (sc *scope) table() string { return domain.StateTable(sc.name) }

// migrationsPath is the scope's migrations directory relative to the
// workspace root (or absolute), as the MigrationSource expects it.
func (sc *scope) migrationsPath() string {
	dir := sc.project.MigrationsDir
	if sc.dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(filepath.FromSlash(sc.dir), dir)
}

// target returns the index of the named target in the scope, or -1.
func (sc *scope) target(name string) int {
	return slices.IndexFunc(sc.project.Targets, func(t domain.Target) bool { return t.Name == name })
}

// Service implements godwit's use cases. It is safe for concurrent use.
//
// A Service drives a plain project, or a workspace of services: the root
// keeps the global targets, and each service its own targets and migrations.
// Targets are addressed with domain.TargetRef: the service gives the
// migrations and state table, and the target is the service's own or, when it
// has none of that name, a global one.
type Service struct {
	source MigrationSource
	dbs    DatabaseFactory

	drivers *domain.Registry

	mu       sync.RWMutex
	root     scope    // the plain project, or the workspace root
	services []*scope // a workspace's services, in the root's order
	current  string   // the service the user works in; "" for all of them

	// globalStore keeps user-wide settings (driver plugins) in ~/.godwit. It is
	// nil when there is no home directory.
	globalStore ProjectStore
	global      domain.Project
}

// New loads the project (or workspace root) from store and returns a Service.
// The services of a workspace are added with LoadService.
func New(store ProjectStore, source MigrationSource, dbs DatabaseFactory) (*Service, error) {
	p, err := store.Load()
	if err != nil {
		return nil, err
	}
	if p.MigrationsDir == "" {
		p.MigrationsDir = defaultMigrationsDir
	}
	return &Service{source: source, dbs: dbs, drivers: newRegistry(dbs), root: scope{store: store, project: p}}, nil
}

// ---- Workspace ----

// LoadService adds an existing service of the workspace, kept in dir
// (relative to the workspace root) with its configuration in store.
func (s *Service) LoadService(name, dir string, store ProjectStore) error {
	if err := domain.ValidateServiceName(name); err != nil {
		return fmt.Errorf("service in %s: %w", dir, err)
	}
	p, err := store.Load()
	if err != nil {
		return fmt.Errorf("service %s: %w", name, err)
	}
	if p.MigrationsDir == "" {
		p.MigrationsDir = defaultMigrationsDir
	}
	p.Service = name
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.services {
		if sc.name == name {
			return fmt.Errorf("services %s and %s are both named %q; set a different \"service\" in one of their .godwit/config.json", sc.dir, dir, name)
		}
	}
	s.services = append(s.services, &scope{name: name, dir: dir, store: store, project: p})
	return nil
}

// AddService makes dir (relative to the workspace root, "/" separated) a
// service named name: it writes the service's configuration to store and
// lists dir in the root's. A plain project becomes a workspace this way; its
// targets become global targets.
func (s *Service) AddService(name, dir string, store ProjectStore) error {
	if err := domain.ValidateServiceName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.services {
		switch {
		case sc.name == name:
			return fmt.Errorf("a service named %q already exists (%s)", name, sc.dir)
		case sc.dir == dir:
			return fmt.Errorf("%s is already the service %q", dir, sc.name)
		}
	}
	p, err := store.Load()
	if err != nil {
		return err
	}
	p.Service = name
	if p.MigrationsDir == "" {
		p.MigrationsDir = defaultMigrationsDir
	}
	if err := store.Save(p); err != nil {
		return err
	}
	if !slices.Contains(s.root.project.Services, dir) {
		s.root.project.Services = append(s.root.project.Services, dir)
		if err := s.root.store.Save(s.root.project); err != nil {
			return err
		}
	}
	s.services = append(s.services, &scope{name: name, dir: dir, store: store, project: p})
	return nil
}

// RemoveService takes a service out of the workspace. Its directory, its
// configuration and its databases are left alone.
func (s *Service) RemoveService(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.services, func(sc *scope) bool { return sc.name == name })
	if i < 0 {
		return s.noServiceLocked(name)
	}
	dir := s.services[i].dir
	s.root.project.Services = slices.DeleteFunc(slices.Clone(s.root.project.Services), func(d string) bool {
		return path.Clean(strings.ReplaceAll(d, `\`, "/")) == dir
	})
	if err := s.root.store.Save(s.root.project); err != nil {
		return err
	}
	s.services = slices.Delete(s.services, i, i+1)
	if s.current == name {
		s.current = ""
	}
	return nil
}

// SetCurrentService makes name the service commands work on by default (the
// directory godwit runs in). "" means every service.
func (s *Service) SetCurrentService(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" && s.serviceLocked(name) == nil {
		return s.noServiceLocked(name)
	}
	s.current = name
	return nil
}

// IsWorkspace reports whether this is a workspace of services rather than a
// plain project.
func (s *Service) IsWorkspace() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspaceLocked()
}

func (s *Service) workspaceLocked() bool {
	return len(s.services) > 0 || s.root.project.IsWorkspace()
}

// Services lists the names of the workspace's services, in order.
func (s *Service) Services() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.services))
	for _, sc := range s.services {
		out = append(out, sc.name)
	}
	return out
}

// ServiceDir returns a service's directory relative to the workspace root.
func (s *Service) ServiceDir(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc := s.serviceLocked(name); sc != nil {
		return sc.dir, true
	}
	return "", false
}

// CurrentService is the service godwit runs in, or "".
func (s *Service) CurrentService() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// DefaultServices lists the services a command acts on when none is named:
// the current service, else every service. A plain project has one unnamed
// scope, [""].
func (s *Service) DefaultServices() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch {
	case !s.workspaceLocked():
		return []string{""}
	case s.current != "":
		return []string{s.current}
	}
	out := make([]string, 0, len(s.services))
	for _, sc := range s.services {
		out = append(out, sc.name)
	}
	return out
}

func (s *Service) serviceLocked(name string) *scope {
	for _, sc := range s.services {
		if sc.name == name {
			return sc
		}
	}
	return nil
}

func (s *Service) noServiceLocked(name string) error {
	if !s.workspaceLocked() {
		return fmt.Errorf("no service %q: this is not a workspace (no \"services\" in .godwit/config.json)", name)
	}
	var names []string
	for _, sc := range s.services {
		names = append(names, sc.name)
	}
	return fmt.Errorf("no service named %q (services: %s)", name, strings.Join(names, ", "))
}

// scopeLocked returns the scope of a service; "" is the plain project or the
// workspace root.
func (s *Service) scopeLocked(service string) (*scope, error) {
	if service == "" {
		return &s.root, nil
	}
	if sc := s.serviceLocked(service); sc != nil {
		return sc, nil
	}
	return nil, s.noServiceLocked(service)
}

// filesScopeLocked is scopeLocked for operations on migration files, which a
// workspace root does not have.
func (s *Service) filesScopeLocked(service string) (*scope, error) {
	if service == "" && s.workspaceLocked() {
		return nil, errors.New("the workspace root has no migrations of its own; name a service")
	}
	return s.scopeLocked(service)
}

// resolveLocked finds the target ref names. ctx is the scope whose
// migrations are used (ref.Service), owner the one that defines the target:
// the service itself or, for a global target, the workspace root.
func (s *Service) resolveLocked(ref domain.TargetRef) (ctx, owner *scope, i int, err error) {
	ctx, err = s.scopeLocked(ref.Service)
	if err != nil {
		return nil, nil, -1, err
	}
	if i = ctx.target(ref.Target); i >= 0 {
		return ctx, ctx, i, nil
	}
	if ctx != &s.root {
		if i = s.root.target(ref.Target); i >= 0 {
			return ctx, &s.root, i, nil
		}
		return nil, nil, -1, fmt.Errorf("no target named %q for service %s", ref.Target, ref.Service)
	}
	return nil, nil, -1, fmt.Errorf("no target named %q", ref.Target)
}

// ---- Project configuration ----

// Drivers lists the supported database drivers.
func (s *Service) Drivers() []string { return s.dbs.Drivers() }

// ScopeTargets returns the targets a service can use: its own, then the
// global ones it does not shadow. Service "" gives the plain project's
// targets, or a workspace's global targets.
func (s *Service) ScopeTargets(service string) []domain.Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, err := s.scopeLocked(service)
	if err != nil {
		return nil
	}
	out := slices.Clone(sc.project.Targets)
	if sc != &s.root {
		for _, g := range s.root.project.Targets {
			if sc.target(g.Name) < 0 {
				out = append(out, g)
			}
		}
	}
	return out
}

// TargetRefs lists every target the default services can use, service by
// service (see DefaultServices). In a plain project it is every target.
func (s *Service) TargetRefs() []domain.TargetRef {
	var out []domain.TargetRef
	for _, svc := range s.DefaultServices() {
		for _, t := range s.ScopeTargets(svc) {
			out = append(out, domain.TargetRef{Service: svc, Target: t.Name})
		}
	}
	return out
}

// Target returns the target ref names.
func (s *Service) Target(ref domain.TargetRef) (domain.Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return domain.Target{}, false
	}
	return owner.project.Targets[i], true
}

// IsGlobal reports whether ref names a global target of a workspace.
func (s *Service) IsGlobal(ref domain.TargetRef) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, owner, _, err := s.resolveLocked(ref)
	return err == nil && owner == &s.root && s.workspaceLocked()
}

// MigrationsDir returns a service's migrations directory as written in its
// configuration (relative to the service directory). Service "" is the plain
// project.
func (s *Service) MigrationsDir(service string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, err := s.scopeLocked(service)
	if err != nil {
		return ""
	}
	return sc.project.MigrationsDir
}

// MigrationsLocation returns a service's migrations directory as an absolute
// path.
func (s *Service) MigrationsLocation(service string) string {
	s.mu.RLock()
	sc, err := s.scopeLocked(service)
	path := ""
	if err == nil {
		path = sc.migrationsPath()
	}
	s.mu.RUnlock()
	return s.source.Resolve(path)
}

// migrationsPath returns the directory the MigrationSource reads a service's
// migrations from.
func (s *Service) migrationsPath(service string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, err := s.filesScopeLocked(service)
	if err != nil {
		return "", err
	}
	return sc.migrationsPath(), nil
}

// SetMigrationsDir changes a service's migrations directory and saves its
// configuration.
func (s *Service) SetMigrationsDir(service, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("migrations directory is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.scopeLocked(service)
	if err != nil {
		return err
	}
	sc.project.MigrationsDir = filepath.Clean(dir)
	return sc.store.Save(sc.project)
}

// EnsureMigrationsDir creates a service's migrations directory if needed.
func (s *Service) EnsureMigrationsDir(service string) error {
	dir, err := s.migrationsPath(service)
	if err != nil {
		return err
	}
	return s.source.EnsureDir(dir)
}

func (s *Service) validate(t domain.Target) error {
	switch {
	case strings.TrimSpace(t.Name) == "":
		return errors.New("target name is required")
	case strings.Contains(t.Name, "/"):
		return errors.New("target names cannot contain \"/\"")
	case !slices.Contains(s.dbs.Drivers(), s.drivers.Normalize(t.Driver)):
		return fmt.Errorf("unknown driver %q (available: %s)", t.Driver, strings.Join(s.dbs.Drivers(), ", "))
	}
	return nil
}

// AddTarget adds a target to a service, or with service "" to the plain
// project or as a global target of a workspace, and saves it. A non-empty
// password is remembered (on disk if persist).
func (s *Service) AddTarget(service string, t domain.Target, password string, persist bool) error {
	t.Name = strings.TrimSpace(t.Name)
	if err := s.validate(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.scopeLocked(service)
	if err != nil {
		return err
	}
	if sc.target(t.Name) >= 0 {
		return fmt.Errorf("a target named %q already exists", t.Name)
	}
	sc.project.Targets = append(sc.project.Targets, t)
	if err := sc.store.Save(sc.project); err != nil {
		return err
	}
	if password != "" {
		return sc.store.SetPassword(t.Name, password, persist)
	}
	return nil
}

// UpdateTarget replaces the target ref names, where it is defined: editing a
// global target changes it for every service. t keeps ref's name. An empty
// password keeps the current one.
func (s *Service) UpdateTarget(ref domain.TargetRef, t domain.Target, password string, persist bool) error {
	if t.Name != ref.Target {
		return fmt.Errorf("target %q cannot be renamed to %q", ref.Target, t.Name)
	}
	if err := s.validate(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return err
	}
	owner.project.Targets[i] = t
	if err := owner.store.Save(owner.project); err != nil {
		return err
	}
	if password != "" {
		return owner.store.SetPassword(t.Name, password, persist)
	}
	return nil
}

// RemoveTarget deletes a target and its stored password. A global target is
// only removed through the workspace root (service ""), not through a service
// that uses it.
func (s *Service) RemoveTarget(ref domain.TargetRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return err
	}
	if owner != ctx {
		return fmt.Errorf("%q is a global target shared by every service; remove it at the workspace root (godwit auth remove %s)", ref.Target, ref.Target)
	}
	owner.project.Targets = slices.Delete(owner.project.Targets, i, i+1)
	if err := owner.store.DeletePassword(ref.Target); err != nil {
		return err
	}
	return owner.store.Save(owner.project)
}

// SetTargetEnabled enables or disables a target and saves it. A disabled
// target keeps its settings and password but is never connected to. For a
// global target this applies to every service.
func (s *Service) SetTargetEnabled(ref domain.TargetRef, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return err
	}
	if owner.project.Targets[i].Disabled == !enabled {
		return nil // already in the requested state
	}
	owner.project.Targets[i].Disabled = !enabled
	return owner.store.Save(owner.project)
}

// MigrationDisabled reports whether a migration version is disabled on ref:
// the target's own override wins over the service's default.
func (s *Service) MigrationDisabled(ref domain.TargetRef, version int64) bool {
	disabled, _ := s.MigrationSetting(ref, version)
	return disabled
}

// MigrationSetting returns whether a version is disabled on ref and why: the
// reason names the target's own override or the default, and is empty when
// the migration is enabled and nothing was overridden.
func (s *Service) MigrationSetting(ref domain.TargetRef, version int64) (disabled bool, reason string) {
	return s.settings(ref)(version)
}

// settings returns MigrationSetting for ref, from a snapshot of the
// configuration, to call once per migration of a status.
func (s *Service) settings(ref domain.TargetRef) func(int64) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return func(int64) (bool, string) { return false, "" }
	}
	p, t := ctx.project, owner.project.Targets[i]
	p.DisabledMigrations = slices.Clone(p.DisabledMigrations)
	t.DisabledMigrations, t.EnabledMigrations = slices.Clone(t.DisabledMigrations), slices.Clone(t.EnabledMigrations)
	return func(version int64) (bool, string) {
		disabled := p.MigrationDisabled(t, version)
		def := slices.Contains(p.DisabledMigrations, version)
		switch {
		case disabled && def:
			return true, "disabled by default"
		case disabled:
			return true, "disabled on this target"
		case def:
			return false, "enabled on this target, disabled by default"
		}
		return false, ""
	}
}

// MigrationDisabledByDefault reports a service's default for a version.
func (s *Service) MigrationDisabledByDefault(service string, version int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, err := s.scopeLocked(service)
	return err == nil && slices.Contains(sc.project.DisabledMigrations, version)
}

// SetMigrationEnabledDefault sets a service's default for a version. It does
// not touch the targets' own overrides, which still win.
func (s *Service) SetMigrationEnabledDefault(service string, version int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.filesScopeLocked(service)
	if err != nil {
		return err
	}
	if slices.Contains(sc.project.DisabledMigrations, version) == !enabled {
		return nil
	}
	sc.project.DisabledMigrations = withVersion(sc.project.DisabledMigrations, version, !enabled)
	return sc.store.Save(sc.project)
}

// SetMigrationEnabled enables or disables one migration version on one target
// and saves the target. A disabled migration is skipped when applying; it
// stays listed, and an already applied one can still be rolled back. The
// choice is stored as an override only when it differs from the service's
// default. The override is kept with the target's definition, so for a global
// target it applies to that version in every service.
func (s *Service) SetMigrationEnabled(ref domain.TargetRef, version int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return err
	}
	t := &owner.project.Targets[i]
	if ctx.project.MigrationDisabled(*t, version) == !enabled {
		return nil
	}
	def := slices.Contains(ctx.project.DisabledMigrations, version)
	t.DisabledMigrations = withVersion(t.DisabledMigrations, version, !enabled && !def)
	t.EnabledMigrations = withVersion(t.EnabledMigrations, version, enabled && def)
	return owner.store.Save(owner.project)
}

// withVersion returns a sorted copy of list with version present or absent.
func withVersion(list []int64, version int64, present bool) []int64 {
	out := slices.DeleteFunc(slices.Clone(list), func(v int64) bool { return v == version })
	if present {
		out = append(out, version)
		slices.Sort(out)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// owner returns the store holding ref's target and the target.
func (s *Service) owner(ref domain.TargetRef) (ProjectStore, domain.Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, owner, i, err := s.resolveLocked(ref)
	if err != nil {
		return nil, domain.Target{}, false
	}
	return owner.store, owner.project.Targets[i], true
}

// HasPassword reports whether a password is available for the target.
func (s *Service) HasPassword(ref domain.TargetRef) bool {
	store, t, ok := s.owner(ref)
	if !ok {
		return false
	}
	_, ok = store.Password(t.Name)
	return ok
}

// NeedsPassword reports whether the target's driver uses a password at all;
// file based plugin drivers do not.
func (s *Service) NeedsPassword(ref domain.TargetRef) bool {
	t, ok := s.Target(ref)
	return ok && !s.DriverNoHost(t.Driver)
}

// PasswordKey is the environment variable that holds the target's password.
func (s *Service) PasswordKey(ref domain.TargetRef) string {
	if store, t, ok := s.owner(ref); ok {
		return store.PasswordKey(t.Name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, err := s.scopeLocked(ref.Service); err == nil {
		return sc.store.PasswordKey(ref.Target)
	}
	return s.root.store.PasswordKey(ref.Target)
}

// TargetsMissingPassword lists enabled targets with no available password,
// once per definition: a global target is listed as itself (no service).
// Disabled targets are never connected to, so they are not asked for one.
func (s *Service) TargetsMissingPassword() []domain.TargetRef {
	var out []domain.TargetRef
	for _, ref := range s.TargetRefs() {
		if s.IsGlobal(ref) {
			ref.Service = ""
		}
		if slices.Contains(out, ref) {
			continue
		}
		t, ok := s.Target(ref)
		if ok && !t.Disabled && s.NeedsPassword(ref) && !s.HasPassword(ref) {
			out = append(out, ref)
		}
	}
	return out
}

// SetPassword remembers a target's password (on disk if persist), where the
// target is defined.
func (s *Service) SetPassword(ref domain.TargetRef, password string, persist bool) error {
	store, t, ok := s.owner(ref)
	if !ok {
		return fmt.Errorf("no target named %q", ref.String())
	}
	return store.SetPassword(t.Name, password, persist)
}

// AuthCommand is the command that addresses ref's target where it is
// defined, for messages: "godwit auth <verb> [-s service] <target>".
func (s *Service) AuthCommand(verb string, ref domain.TargetRef) string {
	if s.IsGlobal(ref) || ref.Service == "" {
		return fmt.Sprintf("godwit auth %s %s", verb, ref.Target)
	}
	return fmt.Sprintf("godwit auth %s -s %s %s", verb, ref.Service, ref.Target)
}

// ---- Migration files ----

// Migrations loads every migration definition of a service, sorted by
// version.
func (s *Service) Migrations(service string) ([]*domain.Migration, error) {
	dir, err := s.migrationsPath(service)
	if err != nil {
		return nil, err
	}
	return s.source.Load(dir)
}

// MigrationFiles lists every migration file of a service, including those
// that failed to load (their Err is set).
func (s *Service) MigrationFiles(service string) ([]domain.MigrationFile, error) {
	dir, err := s.migrationsPath(service)
	if err != nil {
		return nil, err
	}
	return s.source.Scan(dir)
}

// CreateMigration scaffolds a new migration for a service and returns its
// location.
func (s *Service) CreateMigration(service, name string) (string, error) {
	dir, err := s.migrationsPath(service)
	if err != nil {
		return "", err
	}
	return s.source.Create(dir, strings.TrimSpace(name))
}

// ---- Running migrations ----

// open connects to ref's target, recording migrations in its service's state
// table.
func (s *Service) open(ctx context.Context, ref domain.TargetRef) (Database, domain.Target, error) {
	s.mu.RLock()
	sc, owner, i, err := s.resolveLocked(ref)
	var (
		t     domain.Target
		table string
		store ProjectStore
	)
	if err == nil {
		t, table, store = owner.project.Targets[i], sc.table(), owner.store
		if ref.Service == "" && s.workspaceLocked() {
			err = fmt.Errorf("%q is a global target: name the service to migrate on it, as in <service>/%s or with -s <service>", ref.Target, ref.Target)
		}
	}
	s.mu.RUnlock()
	if err != nil {
		return nil, t, err
	}
	if t.Disabled {
		return nil, t, fmt.Errorf("%q: %w (enable it with: %s)", ref.String(), ErrTargetDisabled, s.AuthCommand("enable", ref))
	}
	pw, ok := store.Password(t.Name)
	if !ok && !s.DriverNoHost(t.Driver) {
		return nil, t, fmt.Errorf("no password for target %q (set %s)", ref.String(), store.PasswordKey(t.Name))
	}
	db, err := s.dbs.Open(ctx, t, pw, table)
	return db, t, err
}

// Status lists every known migration with its state on the target. It creates
// the service's state table if the target does not have it yet.
func (s *Service) Status(ctx context.Context, ref domain.TargetRef) ([]domain.Item, error) {
	migs, err := s.Migrations(ref.Service)
	if err != nil {
		return nil, err
	}
	db, _, err := s.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.EnsureTable(ctx); err != nil {
		return nil, err
	}
	recs, err := db.Applied(ctx)
	if err != nil {
		return nil, err
	}
	return merge(migs, recs, s.settings(ref)), nil
}

func merge(migs []*domain.Migration, recs []domain.Record, setting func(int64) (bool, string)) []domain.Item {
	byVer := make(map[int64]*domain.Record, len(recs))
	for i := range recs {
		byVer[recs[i].Version] = &recs[i]
	}
	var items []domain.Item
	for _, m := range migs {
		it := domain.Item{Version: m.Version, Name: m.Name, Migration: m, State: domain.Pending}
		it.Disabled, it.Reason = setting(m.Version)
		if r, ok := byVer[m.Version]; ok {
			it.Record = r
			switch {
			case r.Dirty:
				it.State = domain.Dirty
			case r.Checksum != m.Checksum:
				it.State = domain.Modified
			default:
				it.State = domain.Applied
			}
			delete(byVer, m.Version)
		}
		items = append(items, it)
	}
	for _, r := range byVer {
		it := domain.Item{Version: r.Version, Name: r.Name, Record: r, State: domain.Missing}
		if r.Dirty {
			it.State = domain.Dirty
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	return items
}

// lockedStatus opens the target, takes its lock and returns the current
// status. It refuses to continue while any migration is dirty. release
// unlocks and closes; call it exactly once.
func (s *Service) lockedStatus(ctx context.Context, ref domain.TargetRef) (db Database, items []domain.Item, release func() error, err error) {
	migs, err := s.Migrations(ref.Service)
	if err != nil {
		return nil, nil, nil, err
	}
	db, _, err = s.open(ctx, ref)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (Database, []domain.Item, func() error, error) {
		db.Close()
		return nil, nil, nil, err
	}
	if err := db.EnsureTable(ctx); err != nil {
		return fail(err)
	}
	unlock, err := db.Lock(ctx)
	if err != nil {
		return fail(err)
	}
	release = func() error {
		err := unlock()
		if cerr := db.Close(); err == nil {
			err = cerr
		}
		return err
	}
	recs, err := db.Applied(ctx)
	if err != nil {
		release() //nolint:errcheck
		return nil, nil, nil, err
	}
	items = merge(migs, recs, s.settings(ref))
	for _, it := range items {
		if it.State == domain.Dirty {
			release() //nolint:errcheck
			return nil, nil, nil, fmt.Errorf("migration %d_%s is dirty: a previous run failed part-way; repair the target, then clear the dirty flag", it.Version, it.Name)
		}
	}
	return db, items, release, nil
}

// picker chooses, from a target's status, which migrations a run executes and
// in what order. It runs after the target is locked, so it sees a stable view.
type picker func(items []domain.Item) ([]domain.Item, error)

// execute opens the target, locks it, and runs the migrations chosen by pick
// in the given direction. It returns how many completed. A run stops at the
// first failure.
func (s *Service) execute(ctx context.Context, ref domain.TargetRef, dir domain.Direction, prog Progress, pick picker) (done int, err error) {
	db, items, release, err := s.lockedStatus(ctx, ref)
	if err != nil {
		return 0, err
	}
	defer func() {
		if rerr := release(); err == nil {
			err = rerr
		}
	}()

	todo, err := pick(items)
	if err != nil {
		return 0, err
	}
	if dir == domain.Down { // fail before reverting anything, not half-way through
		for _, it := range todo {
			if it.Migration == nil {
				return 0, fmt.Errorf("cannot revert %d_%s: migration file is missing", it.Version, it.Name)
			}
		}
	}

	for _, it := range todo {
		prog.emit(domain.Event{Direction: dir, Version: it.Version, Name: it.Name, Phase: domain.Queued})
	}
	batch := nextBatch(items)
	for _, it := range todo {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		ev := domain.Event{Direction: dir, Version: it.Version, Name: it.Name, Phase: domain.Started}
		prog.emit(ev)
		if dir == domain.Up {
			m := *it.Migration
			m.Batch = batch
			err = db.Apply(ctx, &m)
		} else {
			err = db.Revert(ctx, it.Migration)
		}
		if err != nil {
			ev.Phase, ev.Err = domain.Failed, err
			prog.emit(ev)
			return done, fmt.Errorf("%d_%s: %w", it.Version, it.Name, err)
		}
		done++
		ev.Phase = domain.Done
		prog.emit(ev)
	}
	return done, nil
}

// pendingItems returns the pending items, which are in version order.
func pendingItems(items []domain.Item) []domain.Item {
	var out []domain.Item
	for _, it := range items {
		if it.State == domain.Pending && !it.Disabled {
			out = append(out, it)
		}
	}
	return out
}

// appliedNewestFirst returns the items recorded on the target, highest version first.
func appliedNewestFirst(items []domain.Item) []domain.Item {
	var out []domain.Item
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Record != nil {
			out = append(out, items[i])
		}
	}
	return out
}

// appliedLatestFirst returns the items recorded on the target, most recently
// applied first. Versions break ties, so a migration created later but applied
// earlier is not treated as the latest.
func appliedLatestFirst(items []domain.Item) []domain.Item {
	out := appliedNewestFirst(items)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Record.AppliedAt.After(out[j].Record.AppliedAt)
	})
	return out
}

// nextBatch returns the number for a new apply run: one past the highest batch
// recorded on the target.
func nextBatch(items []domain.Item) int64 {
	var max int64
	for _, it := range items {
		if it.Record != nil && it.Record.Batch > max {
			max = it.Record.Batch
		}
	}
	return max + 1
}

// LatestBatch returns the migrations of the most recent apply run, newest
// first: the most recently applied migration and every other one recorded in
// the same batch. A migration recorded without a batch (0) stands alone.
func LatestBatch(items []domain.Item) []domain.Item {
	latest := appliedLatestFirst(items)
	if len(latest) == 0 {
		return nil
	}
	batch := latest[0].Record.Batch
	if batch == 0 {
		return latest[:1]
	}
	var out []domain.Item
	for _, it := range latest {
		if it.Record.Batch == batch {
			out = append(out, it)
		}
	}
	return out
}

func findItem(items []domain.Item, version int64) (domain.Item, error) {
	for _, it := range items {
		if it.Version == version {
			return it, nil
		}
	}
	return domain.Item{}, fmt.Errorf("no migration with version %d", version)
}

// Up applies pending migrations in version order. limit <= 0 applies all of
// them. It returns how many were applied.
func (s *Service) Up(ctx context.Context, ref domain.TargetRef, limit int, prog Progress) (int, error) {
	return s.execute(ctx, ref, domain.Up, prog, func(items []domain.Item) ([]domain.Item, error) {
		todo := pendingItems(items)
		if limit > 0 && len(todo) > limit {
			todo = todo[:limit]
		}
		return todo, nil
	})
}

// Down reverts the n most recently applied migrations (n <= 0 means 1), by
// time applied rather than by version. It returns how many were reverted.
func (s *Service) Down(ctx context.Context, ref domain.TargetRef, n int, prog Progress) (int, error) {
	if n <= 0 {
		n = 1
	}
	return s.execute(ctx, ref, domain.Down, prog, func(items []domain.Item) ([]domain.Item, error) {
		todo := appliedLatestFirst(items)
		if len(todo) > n {
			todo = todo[:n]
		}
		return todo, nil
	})
}

// DownBatch reverts the most recent apply run: every migration applied in the
// same batch as the most recently applied one, newest first. It returns how
// many were reverted.
func (s *Service) DownBatch(ctx context.Context, ref domain.TargetRef, prog Progress) (int, error) {
	return s.execute(ctx, ref, domain.Down, prog, func(items []domain.Item) ([]domain.Item, error) {
		return LatestBatch(items), nil
	})
}

// UpTo applies every pending migration up to and including version, in
// version order. The migration must exist and be pending. Pending migrations
// with a higher version are left alone.
func (s *Service) UpTo(ctx context.Context, ref domain.TargetRef, version int64, prog Progress) (int, error) {
	return s.execute(ctx, ref, domain.Up, prog, func(items []domain.Item) ([]domain.Item, error) {
		sel, err := findItem(items, version)
		if err != nil {
			return nil, err
		}
		if sel.State != domain.Pending {
			return nil, fmt.Errorf("%d_%s is already %s; nothing to apply up to it", sel.Version, sel.Name, sel.State)
		}
		if sel.Disabled {
			return nil, fmt.Errorf("%d_%s is disabled on %s; enable it first", sel.Version, sel.Name, ref)
		}
		var todo []domain.Item
		for _, it := range pendingItems(items) {
			if it.Version <= version {
				todo = append(todo, it)
			}
		}
		return todo, nil
	})
}

// ApplyOnly applies the single pending migration with the given version and
// nothing else, even if older migrations are still pending or it is disabled
// on the target. Later migrations that depend on skipped ones may fail.
func (s *Service) ApplyOnly(ctx context.Context, ref domain.TargetRef, version int64, prog Progress) (int, error) {
	return s.execute(ctx, ref, domain.Up, prog, func(items []domain.Item) ([]domain.Item, error) {
		sel, err := findItem(items, version)
		if err != nil {
			return nil, err
		}
		if sel.State != domain.Pending {
			return nil, fmt.Errorf("%d_%s is already %s; use redo to run it again", sel.Version, sel.Name, sel.State)
		}
		return []domain.Item{sel}, nil
	})
}

// DownTo reverts every applied migration newer than version, newest first, so
// that version becomes the most recently applied migration. The migration
// itself stays applied. It must exist and be applied; if it already is the
// newest, nothing is reverted.
func (s *Service) DownTo(ctx context.Context, ref domain.TargetRef, version int64, prog Progress) (int, error) {
	return s.execute(ctx, ref, domain.Down, prog, func(items []domain.Item) ([]domain.Item, error) {
		sel, err := findItem(items, version)
		if err != nil {
			return nil, err
		}
		if sel.Record == nil {
			return nil, fmt.Errorf("%d_%s is not applied on %s; nothing to roll back down to", sel.Version, sel.Name, ref)
		}
		var todo []domain.Item
		for _, it := range appliedNewestFirst(items) {
			if it.Version > version {
				todo = append(todo, it)
			}
		}
		return todo, nil
	})
}

// Redo reverts one applied migration and applies it again, e.g. to pick up an
// edited file. The migration must be applied (or modified) on the target and
// its file must still exist. Later migrations are left alone, so if they
// depend on this one the revert may fail.
//
// If the revert succeeds but the re-apply fails, the migration is left
// reverted (pending, or dirty on engines without transactional DDL) and the
// error says so.
func (s *Service) Redo(ctx context.Context, ref domain.TargetRef, version int64, prog Progress) (err error) {
	db, items, release, err := s.lockedStatus(ctx, ref)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := release(); err == nil {
			err = rerr
		}
	}()

	var it *domain.Item
	for i := range items {
		if items[i].Version == version {
			it = &items[i]
		}
	}
	switch {
	case it == nil:
		return fmt.Errorf("no migration with version %d", version)
	case it.Migration == nil:
		return fmt.Errorf("cannot redo %d_%s: migration file is missing", it.Version, it.Name)
	case it.Record == nil:
		return fmt.Errorf("%d_%s has not been applied on %s; apply it instead", it.Version, it.Name, ref)
	}

	step := func(dir domain.Direction, do func(context.Context, *domain.Migration) error) error {
		ev := domain.Event{Direction: dir, Version: it.Version, Name: it.Name, Phase: domain.Started}
		prog.emit(ev)
		if err := do(ctx, it.Migration); err != nil {
			ev.Phase, ev.Err = domain.Failed, err
			prog.emit(ev)
			return err
		}
		ev.Phase = domain.Done
		prog.emit(ev)
		return nil
	}
	if err := step(domain.Down, db.Revert); err != nil {
		return fmt.Errorf("%d_%s: revert failed: %w", it.Version, it.Name, err)
	}
	reapply := func(ctx context.Context, m *domain.Migration) error {
		again := *m
		again.Batch = it.Record.Batch // it stays in its original batch
		return db.Apply(ctx, &again)
	}
	if err := step(domain.Up, reapply); err != nil {
		return fmt.Errorf("%d_%s: re-apply failed after the revert, so the migration is now reverted: %w", it.Version, it.Name, err)
	}
	return nil
}

// ClearDirty clears a dirty flag after the user repaired the target by hand.
func (s *Service) ClearDirty(ctx context.Context, ref domain.TargetRef, version int64) error {
	db, _, err := s.open(ctx, ref)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ClearDirty(ctx, version)
}

// ForceState rewrites one migration's recorded state on a target without
// running it: pending removes its record, applied writes a clean one, dirty
// marks it as a failed run. Only the state table changes; the schema
// does not. It is a debugging tool for states that are hard to reach otherwise.
func (s *Service) ForceState(ctx context.Context, ref domain.TargetRef, version int64, state domain.State) (err error) {
	switch state {
	case domain.Pending, domain.Applied, domain.Dirty:
	default:
		return fmt.Errorf("cannot force state %q (use pending, applied or dirty)", state)
	}
	migs, err := s.Migrations(ref.Service)
	if err != nil {
		return err
	}
	db, t, err := s.open(ctx, ref)
	if err != nil {
		return err
	}
	defer db.Close()
	forcer, ok := db.(StateForcer)
	if !ok {
		return fmt.Errorf("the %s driver cannot force a migration's state", t.Driver)
	}
	if err := db.EnsureTable(ctx); err != nil {
		return err
	}
	unlock, err := db.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unlock(); err == nil {
			err = uerr
		}
	}()

	recs, err := db.Applied(ctx)
	if err != nil {
		return err
	}
	items := merge(migs, recs, s.settings(ref))
	var it *domain.Item
	for i := range items {
		if items[i].Version == version {
			it = &items[i]
		}
	}
	if it == nil {
		return fmt.Errorf("no migration with version %d", version)
	}
	m := domain.Migration{Version: it.Version, Name: it.Name, Batch: nextBatch(items)}
	switch {
	case it.Migration != nil:
		m.Checksum = it.Migration.Checksum
	case it.Record != nil:
		m.Checksum = it.Record.Checksum
	}
	if it.Record != nil {
		m.Batch = it.Record.Batch
	}
	return forcer.ForceState(ctx, &m, state)
}
