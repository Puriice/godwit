package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslateArgumentErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no tool":      {},
		"unknown tool": {"flyway"},
		"bad flag":     {"goose", "--nope"},
		"no targets":   {"goose"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService(t)
			var out bytes.Buffer
			if err := Translate(context.Background(), svc, args, &out); err == nil {
				t.Errorf("no error; output:\n%s", out.String())
			}
		})
	}
}

func TestTranslateDryRunLeavesFilesAlone(t *testing.T) {
	svc, root := newService(t)
	dir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n"
	if err := os.WriteFile(filepath.Join(dir, "001_x.sql"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := svc.TranslateFiles("goose", true)
	if err != nil || len(files) != 1 || files[0].Status != "converted" || files[0].Checksum == "" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "001_x.sql")); string(got) != src {
		t.Errorf("dry run changed the file:\n%s", got)
	}

	var out bytes.Buffer
	printFiles(&out, files, true)
	if !strings.Contains(out.String(), "would convert: 001_x.sql") {
		t.Errorf("output:\n%s", out.String())
	}
}
