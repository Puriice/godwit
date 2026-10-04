package fsmigrations

import (
	"reflect"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

var testDrivers = []string{"mysql", "postgres"}

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
	p, err := Parse(src, testDrivers)
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
	p, err := Parse("-- +godwit Up\n-- +godwit driver: mysql\nSELECT 1;\n-- +godwit Down\nSELECT 2;\n", testDrivers)
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
		"nested begin":         "-- +godwit Up\n-- +godwit StatementBegin\n-- +godwit StatementBegin\n",
		"directive in block":   "-- +godwit Up\n-- +godwit StatementBegin\n-- +godwit driver: mysql\n",
		"driver mid-statement": "-- +godwit Up\nSELECT\n-- +godwit driver: mysql\n1;\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(src, testDrivers); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestParseUnspecifiedDriverMeansAll(t *testing.T) {
	cases := map[string]string{
		"no directive":        "-- +godwit Up\nSELECT 1;\n",
		"blank with colon":    "-- +godwit Up\n-- +godwit driver:\nSELECT 1;\n",
		"blank without colon": "-- +godwit Up\n-- +godwit driver\nSELECT 1;\n",
		"blank with spaces":   "-- +godwit Up\n-- +godwit driver:   \nSELECT 1;\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := Parse(src, testDrivers)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Up) != 1 || p.Up[0].Driver != driverAll {
				t.Fatalf("up = %+v", p.Up)
			}
			m := &domain.Migration{Up: p.Up}
			if len(m.UpSQL("postgres")) != 1 || len(m.UpSQL("mysql")) != 1 {
				t.Error("statement should run on every driver")
			}
		})
	}

	// A blank directive after a specific one switches back to all.
	p, err := Parse("-- +godwit Up\n-- +godwit driver: mysql\nSELECT 1;\n-- +godwit driver:\nSELECT 2;\n", testDrivers)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Up) != 2 || p.Up[0].Driver != "mysql" || p.Up[1].Driver != driverAll {
		t.Errorf("up = %+v", p.Up)
	}

	// Still an error outside a section, or mid-statement.
	for _, src := range []string{
		"-- +godwit driver:\n-- +godwit Up\n",
		"-- +godwit Up\nSELECT\n-- +godwit driver:\n1;\n",
	} {
		if _, err := Parse(src, testDrivers); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}

func TestParseRejectsUnknownDriver(t *testing.T) {
	cases := map[string]string{
		"typo":              "-- +godwit Up\n-- +godwit driver: postgress\nSELECT 1;\n",
		"unsupported":       "-- +godwit Up\n-- +godwit driver: sqlite\nSELECT 1;\n",
		"no colon":          "-- +godwit Up\n-- +godwit driver oracle\nSELECT 1;\n",
		"in down section":   "-- +godwit Up\nSELECT 1;\n-- +godwit Down\n-- +godwit driver: mssql\nSELECT 2;\n",
		"after valid block": "-- +godwit Up\n-- +godwit driver: mysql\nSELECT 1;\n-- +godwit driver: mysqll\nSELECT 2;\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(src, testDrivers)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "unknown driver") || !strings.Contains(err.Error(), "all, mysql, postgres") {
				t.Errorf("error should name the problem and the valid choices: %v", err)
			}
		})
	}

	// The error points at the offending line.
	_, err := Parse("-- +godwit Up\nSELECT 1;\n-- +godwit driver: postgress\nSELECT 2;\n", testDrivers)
	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), `"postgress"`) {
		t.Errorf("err = %v", err)
	}

	// With no known drivers configured, only "all" is accepted.
	if _, err := Parse("-- +godwit Up\n-- +godwit driver: mysql\nSELECT 1;\n", nil); err == nil {
		t.Error("expected error when no drivers are known")
	}
	if _, err := Parse("-- +godwit Up\n-- +godwit driver: all\nSELECT 1;\n", nil); err != nil {
		t.Errorf("all must always be valid: %v", err)
	}
}

func TestParseAcceptsDriverAliasesAndCase(t *testing.T) {
	p, err := Parse("-- +godwit Up\n-- +godwit driver: PostgreSQL\nSELECT 1;\n-- +godwit driver: MariaDB\nSELECT 2;\n-- +godwit driver: MySQL\nSELECT 3;\n", testDrivers)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range p.Up {
		got = append(got, s.Driver)
	}
	if strings.Join(got, ",") != "postgres,mysql,mysql" {
		t.Errorf("normalized drivers = %v", got)
	}
}
