package plugin

import (
	"context"
	"fmt"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// database is app.Database backed by one plugin process, which owns the
// connection (and so session-scoped locks) for the database's lifetime.
type database struct {
	c      *client
	driver string
}

var _ app.Database = (*database)(nil)

func (d *database) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	d.c.call(ctx, "close", nil, nil) //nolint:errcheck // best effort; the process is stopped next
	return d.c.close()
}

func (d *database) EnsureTable(ctx context.Context) error {
	return d.c.call(ctx, "ensure_table", nil, nil)
}

func (d *database) Lock(ctx context.Context) (func() error, error) {
	if err := d.c.call(ctx, "lock", nil, nil); err != nil {
		return nil, err
	}
	return func() error {
		// The caller's ctx may be cancelled by now; releasing must still work.
		ctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		return d.c.call(ctx, "unlock", nil, nil)
	}, nil
}

func (d *database) Applied(ctx context.Context) ([]domain.Record, error) {
	var res appliedResult
	if err := d.c.call(ctx, "applied", nil, &res); err != nil {
		return nil, err
	}
	out := make([]domain.Record, 0, len(res.Records))
	for _, r := range res.Records {
		out = append(out, domain.Record{Version: r.Version, Name: r.Name, Checksum: r.Checksum,
			AppliedAt: r.AppliedAt, DurationMS: r.DurationMS, Dirty: r.Dirty})
	}
	return out, nil
}

func (d *database) Apply(ctx context.Context, m *domain.Migration) error {
	return d.c.call(ctx, "apply", migrationParams{wire(m, m.UpSQL(d.driver))}, nil)
}

func (d *database) Revert(ctx context.Context, m *domain.Migration) error {
	stmts := m.DownSQL(d.driver)
	if len(stmts) == 0 {
		return fmt.Errorf("migration %d_%s has no Down statements for %s", m.Version, m.Name, d.driver)
	}
	return d.c.call(ctx, "revert", migrationParams{wire(m, stmts)}, nil)
}

func (d *database) ClearDirty(ctx context.Context, version int64) error {
	return d.c.call(ctx, "clear_dirty", clearDirtyParams{Version: version}, nil)
}

func wire(m *domain.Migration, stmts []string) wireMigration {
	if stmts == nil {
		stmts = []string{}
	}
	return wireMigration{Version: m.Version, Name: m.Name, Checksum: m.Checksum,
		NoTransaction: m.NoTransaction, Statements: stmts}
}
