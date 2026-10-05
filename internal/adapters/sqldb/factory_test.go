package sqldb

import (
	"context"
	"slices"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestFactoryDriversAndUnknown(t *testing.T) {
	f := NewFactory()
	if got := f.Drivers(); !slices.Equal(got, []string{"mysql", "postgres", "sqlite"}) {
		t.Errorf("Drivers = %v", got)
	}
	if _, err := f.Open(context.Background(), domain.Target{Driver: "oracle"}, ""); err == nil {
		t.Error("expected unknown driver error")
	}
}

func TestDSNs(t *testing.T) {
	pg := postgresDSN(domain.Target{Host: "h", Database: "d", User: "u", Params: map[string]string{"sslmode": "disable"}}, "p@ss")
	if pg != "postgres://u:p%40ss@h:5432/d?sslmode=disable" {
		t.Errorf("postgres dsn = %s", pg)
	}
	my := mysqlDSN(domain.Target{Host: "h", Port: 3307, Database: "d", User: "u"}, "pw")
	if my != "u:pw@tcp(h:3307)/d?parseTime=true" {
		t.Errorf("mysql dsn = %s", my)
	}
}
