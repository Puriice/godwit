package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseConnectionString(t *testing.T) {
	cases := []struct {
		name, driver, conn string
		want               Target
		pw                 string
	}{
		{
			name: "postgres url", driver: "postgres",
			conn: "postgres://app:p%40ss@db.example.com:5433/shop?sslmode=disable",
			want: Target{Driver: "postgres", Host: "db.example.com", Port: 5433, Database: "shop", User: "app",
				Params: map[string]string{"sslmode": "disable"}},
			pw: "p@ss",
		},
		{
			name: "postgresql alias and scheme, default port", driver: "PostgreSQL",
			conn: "postgresql://u:pw@localhost/d",
			want: Target{Driver: "postgres", Host: "localhost", Database: "d", User: "u"},
			pw:   "pw",
		},
		{
			name: "postgres without password", driver: "postgres",
			conn: "postgres://u@h/d",
			want: Target{Driver: "postgres", Host: "h", Database: "d", User: "u"},
		},
		{
			name: "mysql url", driver: "mysql",
			conn: "mysql://root:secret@127.0.0.1:3306/app",
			want: Target{Driver: "mysql", Host: "127.0.0.1", Port: 3306, Database: "app", User: "root"},
			pw:   "secret",
		},
		{
			name: "mysql dsn", driver: "mysql",
			conn: "godwit:p@ss:word@tcp(localhost:53306)/godwit?charset=utf8mb4&loc=UTC",
			want: Target{Driver: "mysql", Host: "localhost", Port: 53306, Database: "godwit", User: "godwit",
				Params: map[string]string{"charset": "utf8mb4", "loc": "UTC"}},
			pw: "p@ss:word",
		},
		{
			name: "mariadb alias, dsn without port", driver: "mariadb",
			conn: "u:pw@tcp(db)/x",
			want: Target{Driver: "mysql", Host: "db", Database: "x", User: "u"},
			pw:   "pw",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, pw, err := ParseConnectionString(c.driver, c.conn)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) || pw != c.pw {
				t.Errorf("got %+v pw=%q\nwant %+v pw=%q", got, pw, c.want, c.pw)
			}
		})
	}
}

func TestParseConnectionStringErrors(t *testing.T) {
	cases := map[string][2]string{
		"unknown driver":     {"oracle", "oracle://u:p@h/d"},
		"scheme mismatch":    {"postgres", "mysql://u:p@h/d"},
		"postgres needs url": {"postgres", "u:p@tcp(h)/d"},
		"no host":            {"postgres", "postgres://u:p@/d"},
		"no database":        {"postgres", "postgres://u:p@h"},
		"no user":            {"postgres", "postgres://h/d"},
		"bad port":           {"postgres", "postgres://u:p@h:99999/d"},
		"bad mysql dsn":      {"mysql", "not a dsn"},
		"mysql unix socket":  {"mysql", "u:p@unix(/tmp/mysql.sock)/d"},
		"empty":              {"mysql", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseConnectionString(c[0], c[1]); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestParseErrorsNeverEchoPassword(t *testing.T) {
	for _, conn := range []string{
		"postgres://u:hunter2@h:notaport/d",
		"postgres://u:hunter2@h:99999/d",
		"mysql://u:hunter2@[::1/d",
		"hunter2@nope",
	} {
		_, _, err := ParseConnectionString("postgres", conn)
		if err == nil {
			t.Fatalf("%q: expected error", conn)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: error leaks password: %v", conn, err)
		}
	}
}

func TestRedactedString(t *testing.T) {
	tg := Target{Driver: "postgres", Host: "db", Port: 5433, Database: "shop", User: "app",
		Params: map[string]string{"sslmode": "disable", "application_name": "godwit"}}
	if got, want := tg.RedactedString(true), "postgres://app:****@db:5433/shop?application_name=godwit&sslmode=disable"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := (Target{Driver: "mysql", Host: "h", Database: "d", User: "u"}).RedactedString(false), "mysql://u@h/d"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
