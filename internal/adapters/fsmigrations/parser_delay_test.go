package fsmigrations

import (
	"testing"
	"time"
)

func TestParseDelay(t *testing.T) {
	cases := map[string]time.Duration{
		"500ms":  500 * time.Millisecond,
		"5s":     5 * time.Second,
		"2m":     2 * time.Minute,
		"1hr":    time.Hour,
		"1d":     24 * time.Hour,
		"1.5s":   1500 * time.Millisecond,
		"500 ms": 500 * time.Millisecond,
		"  3  S": 3 * time.Second,
		"0s":     0,
	}
	for in, want := range cases {
		got, err := ParseDelay(in)
		if err != nil || got != want {
			t.Errorf("ParseDelay(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "5", "s", "5h", "5 sec", "-5s", "1e3s", "5s5", "99999999999999d"} {
		if _, err := ParseDelay(in); err == nil {
			t.Errorf("ParseDelay(%q) should fail", in)
		}
	}
}

func TestParseRepeatStartDelay(t *testing.T) {
	for name, line := range map[string]string{
		"colon and space":     "-- +godwit RepeatStart: 250ms",
		"colon, no space":     "-- +godwit RepeatStart:250ms",
		"space, no colon":     "-- +godwit RepeatStart 250ms",
		"space before unit":   "-- +godwit RepeatStart: 250 ms",
		"mixed case, comment": "-- +godwit repeatSTART:250MS -- be gentle",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Parse("-- +godwit Up\n"+line+"\nUPDATE t SET a = 1;\nUPDATE t SET b = 1;\n-- +godwit RepeatEnd\nSELECT 1;\n", testDrivers)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Up) != 3 || p.Up[0].Delay != 250*time.Millisecond || p.Up[1].Delay != 250*time.Millisecond {
				t.Errorf("delays = %+v", p.Up)
			}
			if p.Up[2].Delay != 0 {
				t.Errorf("delay leaked out of the block: %+v", p.Up[2])
			}
		})
	}
}

func TestParseRepeatStartWithoutDelay(t *testing.T) {
	for _, line := range []string{"-- +godwit RepeatStart", "-- +godwit RepeatStart:", "-- +godwit RepeatStart:  "} {
		p, err := Parse("-- +godwit Up\n"+line+"\nUPDATE t SET a = 1;\n-- +godwit RepeatEnd\n", testDrivers)
		if err != nil || len(p.Up) != 1 || p.Up[0].Delay != 0 || p.Up[0].Group == 0 {
			t.Errorf("%q: %+v, %v", line, p, err)
		}
	}
}

func TestParseRepeatStartDelayPerBlock(t *testing.T) {
	src := "-- +godwit Up\n-- +godwit RepeatStart: 1s\nSELECT 1;\n-- +godwit RepeatEnd\n" +
		"-- +godwit RepeatStart\nSELECT 2;\n-- +godwit RepeatEnd\n" +
		"-- +godwit RepeatStart: 2m\nSELECT 3;\n-- +godwit RepeatEnd\n"
	p, err := Parse(src, testDrivers)
	if err != nil {
		t.Fatal(err)
	}
	if p.Up[0].Delay != time.Second || p.Up[1].Delay != 0 || p.Up[2].Delay != 2*time.Minute {
		t.Errorf("delays = %+v", p.Up)
	}
}

func TestParseRepeatStartDelayErrors(t *testing.T) {
	for name, line := range map[string]string{
		"no unit":         "-- +godwit RepeatStart: 5",
		"bad unit":        "-- +godwit RepeatStart: 5 hours",
		"not a number":    "-- +godwit RepeatStart: soon",
		"negative":        "-- +godwit RepeatStart: -5s",
		"unit only":       "-- +godwit RepeatStart: ms",
		"old RepeatDelay": "-- +godwit RepeatDelay: 5s",
	} {
		t.Run(name, func(t *testing.T) {
			src := "-- +godwit Up\n" + line + "\nSELECT 1;\n-- +godwit RepeatEnd\n"
			if _, err := Parse(src, testDrivers); err == nil {
				t.Error("expected error")
			}
		})
	}
}
