package fsmigrations

import (
	"reflect"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestParseRepeat(t *testing.T) {
	src := `-- +godwit Up
CREATE TABLE a (id INT);
-- +godwit RepeatStart
UPDATE a SET x = 1;
-- +godwit driver: mysql
UPDATE a SET y = 2 LIMIT 10;
-- +godwit driver: all
-- +godwit StatementBegin
DELETE FROM a;
-- +godwit StatementEnd
-- +godwit RepeatCondition
SELECT EXISTS (SELECT 1 FROM a);
-- +godwit RepeatEnd
DROP TABLE b;
-- +godwit RepeatStart
UPDATE b SET z = 1;
-- +godwit RepeatEnd
`
	p, err := Parse(src, testDrivers)
	if err != nil {
		t.Fatal(err)
	}
	groups := make([]int, len(p.Up))
	for i, s := range p.Up {
		groups[i] = s.Group
	}
	if want := []int{0, 1, 1, 1, 1, 0, 2}; !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups = %v, want %v", groups, want)
	}
	if !p.Up[4].Condition || p.Up[1].Condition || p.Up[3].Condition {
		t.Errorf("condition flags: %+v", p.Up)
	}

	m := &domain.Migration{Up: p.Up}
	steps := m.UpSteps("postgres")
	if len(steps) != 4 || !steps[1].Repeat || len(steps[1].SQL) != 2 ||
		!strings.HasPrefix(steps[1].Condition, "SELECT EXISTS") || steps[2].Repeat || steps[3].Condition != "" {
		t.Errorf("postgres steps = %+v", steps)
	}
	if steps := m.UpSteps("mysql"); len(steps[1].SQL) != 3 {
		t.Errorf("mysql block body = %v", steps[1].SQL)
	}
}

func TestParseRepeatErrors(t *testing.T) {
	cases := map[string]string{
		"repeat outside section":     "-- +godwit RepeatStart\n",
		"nested repeat":              "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n-- +godwit RepeatStart\n",
		"repeat end without start":   "-- +godwit Up\n-- +godwit RepeatEnd\n",
		"repeat never ended":         "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n",
		"repeat open at section end": "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n-- +godwit Down\n",
		"empty repeat":               "-- +godwit Up\n-- +godwit RepeatStart\n-- +godwit RepeatEnd\n",
		"condition outside repeat":   "-- +godwit Up\n-- +godwit RepeatCondition\nSELECT 1;\n",
		"condition first":            "-- +godwit Up\n-- +godwit RepeatStart\n-- +godwit RepeatCondition\nSELECT 1;\n-- +godwit RepeatEnd\n",
		"condition without query":    "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n-- +godwit RepeatCondition\n-- +godwit RepeatEnd\n",
		"two conditions":             "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n-- +godwit RepeatCondition\nSELECT 2;\n-- +godwit RepeatCondition\nSELECT 3;\n-- +godwit RepeatEnd\n",
		"statement after condition":  "-- +godwit Up\n-- +godwit RepeatStart\nSELECT 1;\n-- +godwit RepeatCondition\nSELECT 2;\nSELECT 3;\n-- +godwit RepeatEnd\n",
		"repeat in statement block":  "-- +godwit Up\n-- +godwit StatementBegin\n-- +godwit RepeatStart\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(src, testDrivers); err == nil {
				t.Error("expected error")
			}
		})
	}
}
