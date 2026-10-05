package sqldb_test

import (
	"context"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

func TestSQLiteForceState(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	f, ok := db.(app.StateForcer)
	if !ok {
		t.Fatal("sqlite connection should implement StateForcer")
	}
	m := &domain.Migration{Version: 5, Name: "x", Checksum: "sum", Batch: 3}
	row := func() (n, dirty int) {
		t.Helper()
		n = count(t, raw, "SELECT COUNT(*) FROM "+"godwit_migration WHERE version = 5")
		if n > 0 {
			dirty = count(t, raw, "SELECT dirty FROM godwit_migration WHERE version = 5")
		}
		return n, dirty
	}
	steps := []struct {
		to        domain.State
		rows, bad int
	}{
		{domain.Pending, 0, 0}, // nothing to remove is fine
		{domain.Applied, 1, 0}, // pending -> applied inserts a clean row
		{domain.Applied, 1, 0}, // same state again changes nothing
		{domain.Dirty, 1, 1},   // applied -> dirty
		{domain.Applied, 1, 0}, // dirty -> applied
		{domain.Dirty, 1, 1},
		{domain.Pending, 0, 0}, // dirty -> pending removes the row
		{domain.Dirty, 1, 1},   // pending -> dirty inserts a dirty row
	}
	for _, s := range steps {
		if err := f.ForceState(ctx, m, s.to); err != nil {
			t.Fatalf("to %s: %v", s.to, err)
		}
		if n, dirty := row(); n != s.rows || dirty != s.bad {
			t.Fatalf("after forcing %s: rows=%d dirty=%d, want rows=%d dirty=%d", s.to, n, dirty, s.rows, s.bad)
		}
	}

	rs, err := db.Applied(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "x" || rs[0].Checksum != "sum" || rs[0].Batch != 3 || !rs[0].Dirty {
		t.Errorf("Applied = %+v, %v", rs, err)
	}

	if err := f.ForceState(ctx, m, domain.Modified); err == nil {
		t.Error("forcing the derived state modified should be refused")
	}
}
