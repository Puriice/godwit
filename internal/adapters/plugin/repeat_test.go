package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestPluginRefusesRepeatBlocks(t *testing.T) {
	d := &database{driver: "x"}
	stmts := []domain.Statement{{Driver: domain.DriverAll, SQL: "SELECT 1;", Group: 1}}
	m := &domain.Migration{Version: 1, Name: "r", Up: stmts, Down: stmts}
	for name, err := range map[string]error{
		"apply":  d.Apply(context.Background(), m),
		"revert": d.Revert(context.Background(), m),
	} {
		if err == nil || !strings.Contains(err.Error(), "RepeatStart") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
