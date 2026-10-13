package domain

import (
	"strings"
	"testing"
	"time"
)

func TestConvertGoose(t *testing.T) {
	tests := []struct {
		name, in, want string
		changed        bool
		warns          int
	}{
		{"up down", "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n",
			"-- +goose Up\n-- +godwit Up\nSELECT 1;\n-- +goose Down\n-- +godwit Down\nSELECT 2;\n", true, 0},
		{"statement block puts End before the goose line",
			"-- +goose Up\n-- +goose StatementBegin\nX;\n-- +goose StatementEnd\n",
			"-- +goose Up\n-- +godwit Up\n-- +goose StatementBegin\n-- +godwit StatementBegin\nX;\n-- +godwit StatementEnd\n-- +goose StatementEnd\n", true, 0},
		{"no transaction", "-- +goose NO TRANSACTION\n-- +goose Up\nX;\n",
			"-- +goose NO TRANSACTION\n-- +godwit NoTransaction\n-- +goose Up\n-- +godwit Up\nX;\n", true, 0},
		{"case and CRLF", "-- +goose up\r\nX;\r\n",
			"-- +goose up\r\n-- +godwit Up\r\nX;\r\n", true, 0},
		{"envsub warns", "-- +goose Up\n-- +goose ENVSUB ON\nX;\n",
			"-- +goose Up\n-- +godwit Up\n-- +goose ENVSUB ON\nX;\n", true, 1},
		{"already converted", "-- +goose Up\n-- +godwit Up\nX;\n", "-- +goose Up\n-- +godwit Up\nX;\n", false, 0},
		{"plain sql", "SELECT 1;\n", "SELECT 1;\n", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warns, changed := ConvertGoose(tt.in)
			if got != tt.want || changed != tt.changed || len(warns) != tt.warns {
				t.Errorf("got %q changed=%v warns=%v\nwant %q changed=%v", got, changed, warns, tt.want, tt.changed)
			}
			if again, _, ch := ConvertGoose(got); again != got || (ch && strings.Contains(got, godwitMarker)) {
				t.Errorf("not idempotent: %q", again)
			}
		})
	}
}

func TestGooseApplied(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }
	got := GooseApplied([]ForeignRow{
		{0, true, at(0)}, {1, true, at(1)}, {2, true, at(2)}, {2, false, at(3)}, {2, true, at(4)}, {3, true, at(5)}, {3, false, at(6)},
	})
	if len(got) != 2 || !got[1].Equal(at(1)) || !got[2].Equal(at(4)) {
		t.Errorf("got %v", got)
	}
}

func TestParseMigrationFileName(t *testing.T) {
	if v, n, ok := ParseMigrationFileName("00012_add_users.sql"); !ok || v != 12 || n != "add_users" {
		t.Errorf("got %d %q %v", v, n, ok)
	}
	for _, bad := range []string{"a_b.sql", "1_a.b.sql", "1_x.go", "x.sql"} {
		if _, _, ok := ParseMigrationFileName(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
