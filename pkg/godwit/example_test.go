package godwit_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/puriice/godwit/pkg/godwit"
)

// fileDriver is the smallest useful driver: a database that is one file.
type fileDriver struct{}

func (fileDriver) Info() godwit.Info {
	return godwit.Info{Name: "filedb", Schemes: []string{"filedb"}, NoHost: true}
}

func (fileDriver) ParseConnection(conn string) (godwit.Target, string, error) {
	path, ok := strings.CutPrefix(conn, "filedb://")
	if !ok || path == "" {
		return godwit.Target{}, "", fmt.Errorf("expected filedb://<file>")
	}
	return godwit.Target{Database: path}, "", nil
}

func (fileDriver) Open(ctx context.Context, t godwit.Target, password string) (godwit.Connection, error) {
	// Open t.Database with your database library and return a Connection that
	// implements EnsureTable, Lock, Applied, Apply, Revert and ClearDirty.
	return nil, fmt.Errorf("not implemented")
}

// A plugin's main function is a single call. Build the result as
// godwit-driver-<name> and register it with: godwit plugin add <name> <command>.
func ExampleServe() {
	if err := godwit.Serve(fileDriver{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
