package plugin

import (
	"context"
	"fmt"
	"slices"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// database is app.Database backed by one plugin process, which owns the
// connection (and so session-scoped locks) for the database's lifetime.
type database struct {
	c      *client
	driver string
	// translate is what the plugin's handshake offered (see handshakeResult).
	translate []string
}

var (
	_ app.Database        = (*database)(nil)
	_ app.ForeignImporter = (*database)(nil)
)

func (d *database) canTranslate(tool string) error {
	if !slices.Contains(d.translate, tool) {
		return fmt.Errorf("the %s plugin does not support importing from %s", d.driver, tool)
	}
	return nil
}

func (d *database) ForeignApplied(ctx context.Context, tool, table string) ([]domain.ForeignRow, bool, error) {
	if err := d.canTranslate(tool); err != nil {
		return nil, false, err
	}
	var res translateReadResult
	if err := d.c.call(ctx, "translate_read", translateReadParams{Tool: tool, Table: table}, &res); err != nil {
		return nil, false, err
	}
	out := make([]domain.ForeignRow, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, domain.ForeignRow{Version: r.Version, Applied: r.Applied, At: r.At})
	}
	return out, res.Found, nil
}

func (d *database) ImportRecords(ctx context.Context, recs []domain.Record) (int, error) {
	if len(d.translate) == 0 {
		return 0, fmt.Errorf("the %s plugin does not support importing history", d.driver)
	}
	wire := make([]wireRecord, 0, len(recs))
	for _, r := range recs {
		wire = append(wire, wireRecord{Version: r.Version, Name: r.Name, Checksum: r.Checksum,
			AppliedAt: r.AppliedAt, DurationMS: r.DurationMS, Dirty: r.Dirty, Batch: r.Batch})
	}
	var res translateImportResult
	if err := d.c.call(ctx, "translate_import", translateImportParams{Records: wire}, &res); err != nil {
		return 0, err
	}
	return res.Added, nil
}

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
			AppliedAt: r.AppliedAt, DurationMS: r.DurationMS, Dirty: r.Dirty, Batch: r.Batch})
	}
	return out, nil
}

func (d *database) Apply(ctx context.Context, m *domain.Migration) error {
	if m.UpHasRepeat() {
		return fmt.Errorf("migration %d_%s uses RepeatStart, which plugin drivers do not support", m.Version, m.Name)
	}
	return d.c.call(ctx, "apply", migrationParams{wire(m, m.UpSQL(d.driver))}, nil)
}

func (d *database) Revert(ctx context.Context, m *domain.Migration) error {
	if m.DownHasRepeat() {
		return fmt.Errorf("migration %d_%s uses RepeatStart, which plugin drivers do not support", m.Version, m.Name)
	}
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
		NoTransaction: m.NoTransaction, Statements: stmts, Batch: m.Batch}
}
