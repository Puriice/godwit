package internal_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "github.com/puriice/godwit/internal/"

// Dependencies point inward: adapters -> app -> domain. The core never
// imports an adapter, and adapters never import each other.
var allowed = map[string][]string{
	"domain":                {},
	"app":                   {"domain"},
	"adapters/filestore":    {"domain", "app"},
	"adapters/fsmigrations": {"domain", "app"},
	"adapters/sqldb":        {"domain", "app"},
	"adapters/tui":          {"domain", "app"},
	"adapters/cli":          {"domain", "app"},
}

func TestDependenciesPointInward(t *testing.T) {
	for pkg, ok := range allowed {
		files, _ := filepath.Glob(filepath.Join(filepath.FromSlash(pkg), "*.go"))
		if len(files) == 0 {
			t.Fatalf("no files for %s", pkg)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue // tests may wire real adapters together
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(path, mod) {
					continue
				}
				dep := strings.TrimPrefix(path, mod)
				if !contains(ok, dep) {
					t.Errorf("%s imports %s; %s may only import %v", f, dep, pkg, ok)
				}
			}
		}
	}
}

func TestEveryPackageHasARule(t *testing.T) {
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		files, _ := filepath.Glob(filepath.Join(path, "*.go"))
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				if _, ok := allowed[filepath.ToSlash(path)]; !ok {
					t.Errorf("package %s has no dependency rule in architecture_test.go", path)
				}
				break
			}
		}
		return nil
	})
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
