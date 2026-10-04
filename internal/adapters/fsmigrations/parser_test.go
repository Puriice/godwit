package fsmigrations

import (
	"reflect"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestParse(t *testing.T) {
	src := `-- header comment
-- +godwit Up
-- +godwit driver: all   -- default
CREATE TABLE users (id BIGINT PRIMARY KEY);

-- +godwit driver: postgres
CREATE EXTENSION IF NOT EXISTS citext;

-- +godwit driver: MySQL
ALTER TABLE users
  ENGINE=InnoDB;

-- +godwit driver all
-- +godwit StatementBegin
CREATE FUNCTION f() RETURNS int AS $$
BEGIN
  RETURN 1;
END;
$$ LANGUAGE plpgsql;
-- +godwit StatementEnd

-- +godwit Down
DROP TABLE users;
-- +godwit NoTransaction
`
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !p.NoTransaction {
		t.Error("NoTransaction not set")
	}
	if len(p.Up) != 4 || len(p.Down) != 1 {
		t.Fatalf("got %d up, %d down", len(p.Up), len(p.Down))
	}
	if p.Up[1].Driver != "postgres" || p.Up[2].Driver != "mysql" || p.Up[3].Driver != driverAll {
		t.Errorf("drivers: %+v", p.Up)
	}
	if !strings.Contains(p.Up[3].SQL, "RETURN 1;") {
		t.Errorf("block body split: %q", p.Up[3].SQL)
	}

	got := (&domain.Migration{Up: p.Up}).UpSQL("MySQL")
	if len(got) != 3 {
		t.Errorf("mysql statements = %d, want 3: %v", len(got), got)
	}
	got = (&domain.Migration{Up: p.Up}).UpSQL("postgres")
	if len(got) != 3 {
		t.Errorf("postgres statements = %d, want 3: %v", len(got), got)
	}
}

func TestParseDriverResetsPerSection(t *testing.T) {
	p, err := Parse("-- +godwit Up\n-- +godwit driver: mysql\nSELECT 1;\n-- +godwit Down\nSELECT 2;\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Down, []Statement{{Driver: driverAll, SQL: "SELECT 2;"}}) {
		t.Errorf("down = %+v", p.Down)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown directive":    "-- +godwit Up\n-- +godwit bogus\n",
		"sql before up":        "SELECT 1;\n-- +godwit Up\n",
		"no up":                "-- just a comment\n",
		"missing semicolon":    "-- +godwit Up\nSELECT 1\n",
		"unterminated block":   "-- +godwit Up\n-- +godwit StatementBegin\nSELECT 1;\n",
		"end without begin":    "-- +godwit Up\n-- +godwit StatementEnd\n",
		"driver outside":       "-- +godwit driver: mysql\n-- +godwit Up\n",
		"driver without name":  "-- +godwit Up\n-- +godwit driver:\n",
		"nested begin":         "-- +godwit Up\n-- +godwit StatementBegin\n-- +godwit StatementBegin\n",
		"directive in block":   "-- +godwit Up\n-- +godwit StatementBegin\n-- +godwit driver: mysql\n",
		"driver mid-statement": "-- +godwit Up\nSELECT\n-- +godwit driver: mysql\n1;\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(src); err == nil {
				t.Error("expected error")
			}
		})
	}
}
