// Package app holds godwit's use cases. It defines the ports (interfaces) the
// outside world must implement and depends only on the domain package.
//
// Driven (outbound) ports, implemented by adapters under internal/adapters:
// ProjectStore, MigrationSource, DatabaseFactory/Database.
// The driving (inbound) port is Service itself, used by the TUI and CLI.
package app

import (
	"context"

	"github.com/puriice/godwit/internal/domain"
)

// ProjectStore persists project configuration and per-target secrets.
type ProjectStore interface {
	Load() (domain.Project, error)
	Save(domain.Project) error
	// Password resolves a target's password; ok is false if none is known.
	Password(target string) (pw string, ok bool)
	// SetPassword remembers a password. persist=false keeps it for this
	// session only.
	SetPassword(target, pw string, persist bool) error
	// DeletePassword forgets a target's stored password.
	DeletePassword(target string) error
}

// MigrationSource reads and creates migration definitions. dir is the
// project's configured migrations directory.
type MigrationSource interface {
	Load(dir string) ([]*domain.Migration, error)
	// Create scaffolds a new, empty migration and returns where it was put.
	Create(dir, name string) (location string, err error)
	// Resolve returns dir as a displayable absolute location.
	Resolve(dir string) string
	// EnsureDir makes sure the migrations directory exists.
	EnsureDir(dir string) error
}

// DatabaseFactory opens connections to targets.
type DatabaseFactory interface {
	// Drivers lists the supported driver names.
	Drivers() []string
	Open(ctx context.Context, t domain.Target, password string) (Database, error)
}

// Database is an open connection to one target. It owns the target's
// godwit_migration state table.
type Database interface {
	Close() error
	// EnsureTable creates the state table if it does not exist.
	EnsureTable(ctx context.Context) error
	// Lock takes a target-wide migration lock; call the returned func to release.
	Lock(ctx context.Context) (unlock func() error, err error)
	Applied(ctx context.Context) ([]domain.Record, error)
	Apply(ctx context.Context, m *domain.Migration) error
	Revert(ctx context.Context, m *domain.Migration) error
	// ClearDirty removes a version's dirty flag after manual repair.
	ClearDirty(ctx context.Context, version int64) error
}
