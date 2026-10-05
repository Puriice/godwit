// Package fsmigrations is the driven adapter that reads migrations from .sql
// files on disk, in godwit's goose-style annotated format.
package fsmigrations

import (
	"bufio"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/domain"
)

const (
	prefix    = "-- +godwit"
	driverAll = domain.DriverAll
)

// Statement is a single SQL statement scoped to a driver.
type Statement = domain.Statement

// Parsed is the result of parsing one migration file.
type Parsed struct {
	Up            []Statement
	Down          []Statement
	NoTransaction bool
}

type section int

const (
	secNone section = iota
	secUp
	secDown
)

var delayRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m|hr|d)$`)

var delayUnits = map[string]time.Duration{
	"ms": time.Millisecond,
	"s":  time.Second,
	"m":  time.Minute,
	"hr": time.Hour,
	"d":  24 * time.Hour,
}

// ParseDelay reads the delay of a RepeatStart directive such as "500ms", "2 s", "1.5m", "1hr" or
// "1d". The number and unit may be separated by spaces or not, and the unit is
// not case-sensitive.
func ParseDelay(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	m := delayRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid delay %q (use a number and a unit: ms, s, m, hr or d, e.g. 500ms)", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid delay %q: %w", s, err)
	}
	f := n * float64(delayUnits[m[2]])
	if f >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("delay %q is too long", s)
	}
	return time.Duration(f), nil
}

// Parse reads a migration file's contents. drivers lists the driver names a
// "driver:" directive may use (besides "all"); anything else is an error, so a
// typo cannot silently make statements run nowhere.
func Parse(src string, drivers []string) (*Parsed, error) {
	return ParseWith(src, drivers, domain.NormalizeDriver)
}

// ParseWith is Parse with a custom driver-alias normalizer, so plugin drivers
// can be written under their aliases too.
func ParseWith(src string, drivers []string, normalize func(string) string) (*Parsed, error) {
	p := &Parsed{}
	var (
		sec     = secNone
		driver  = driverAll
		block   bool // inside StatementBegin/End
		buf     strings.Builder
		lineNum int

		group    int  // id of the open RepeatStart block, 0 outside one
		lastID   int  // last group id handed out
		body     int  // body statements in the open block
		condWait bool // RepeatCondition seen, its statement still to come
		condDone bool // the open block's condition statement is in

		delay time.Duration // the open block's delay between passes
	)

	flush := func() error {
		sql := strings.TrimSpace(buf.String())
		buf.Reset()
		if sql == "" {
			return nil
		}
		st := Statement{Driver: driver, SQL: sql, Group: group, Delay: delay}
		switch {
		case condWait:
			st.Condition = true
			condWait, condDone = false, true
		case condDone:
			return fmt.Errorf("statement after RepeatCondition (it must be the last statement in the block)")
		case group != 0:
			body++
		}
		if sec == secUp {
			p.Up = append(p.Up, st)
		} else {
			p.Down = append(p.Down, st)
		}
		return nil
	}

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			d := strings.TrimSpace(rest)
			if block && !strings.EqualFold(d, "StatementEnd") {
				return nil, fmt.Errorf("line %d: directive inside StatementBegin block", lineNum)
			}
			// Drop trailing comment, e.g. "driver: all -- default".
			if i := strings.Index(d, "--"); i >= 0 {
				d = strings.TrimSpace(d[:i])
			}
			key, val, _ := strings.Cut(d, ":")
			key = strings.ToLower(strings.TrimSpace(key))
			val = strings.ToLower(strings.TrimSpace(val))
			// Allow "driver mysql" as well as "driver: mysql", and the same for
			// "RepeatStart 500ms".
			if k, v, ok := strings.Cut(key, " "); ok && (k == "driver" || k == "repeatstart") {
				key, val = k, strings.TrimSpace(v)
			}

			if condWait && key != "statementbegin" {
				return nil, fmt.Errorf("line %d: RepeatCondition must be followed by a statement", lineNum)
			}

			switch key {
			case "repeatstart":
				switch {
				case sec == secNone:
					return nil, fmt.Errorf("line %d: RepeatStart outside Up/Down", lineNum)
				case group != 0:
					return nil, fmt.Errorf("line %d: nested RepeatStart", lineNum)
				case strings.TrimSpace(buf.String()) != "":
					return nil, fmt.Errorf("line %d: RepeatStart after unterminated statement", lineNum)
				}
				// An optional delay between passes: "RepeatStart: 500ms".
				delay = 0
				if val != "" {
					d, err := ParseDelay(val)
					if err != nil {
						return nil, fmt.Errorf("line %d: RepeatStart: %w", lineNum, err)
					}
					delay = d
				}
				lastID++
				group, body, condDone = lastID, 0, false
			case "repeatcondition":
				switch {
				case group == 0:
					return nil, fmt.Errorf("line %d: RepeatCondition outside RepeatStart/RepeatEnd", lineNum)
				case condDone:
					return nil, fmt.Errorf("line %d: more than one RepeatCondition in a block", lineNum)
				case body == 0:
					return nil, fmt.Errorf("line %d: RepeatCondition before any statement in the block", lineNum)
				case strings.TrimSpace(buf.String()) != "":
					return nil, fmt.Errorf("line %d: RepeatCondition after unterminated statement", lineNum)
				}
				condWait = true
			case "repeatend":
				switch {
				case group == 0:
					return nil, fmt.Errorf("line %d: RepeatEnd without RepeatStart", lineNum)
				case strings.TrimSpace(buf.String()) != "":
					return nil, fmt.Errorf("line %d: RepeatEnd after unterminated statement", lineNum)
				case body == 0:
					return nil, fmt.Errorf("line %d: empty RepeatStart/RepeatEnd block", lineNum)
				}
				group, condDone = 0, false
				delay = 0
			case "up", "down":
				if buf.Len() > 0 && strings.TrimSpace(buf.String()) != "" {
					return nil, fmt.Errorf("line %d: unterminated statement before %s", lineNum, key)
				}
				if group != 0 {
					return nil, fmt.Errorf("line %d: %s inside RepeatStart without RepeatEnd", lineNum, key)
				}
				buf.Reset()
				if key == "up" {
					sec = secUp
				} else {
					sec = secDown
				}
				driver = driverAll
			case "statementbegin":
				if sec == secNone {
					return nil, fmt.Errorf("line %d: StatementBegin outside Up/Down", lineNum)
				}
				if block {
					return nil, fmt.Errorf("line %d: nested StatementBegin", lineNum)
				}
				if strings.TrimSpace(buf.String()) != "" {
					return nil, fmt.Errorf("line %d: StatementBegin after unterminated statement", lineNum)
				}
				block = true
			case "statementend":
				if !block {
					return nil, fmt.Errorf("line %d: StatementEnd without StatementBegin", lineNum)
				}
				block = false
				if err := flush(); err != nil {
					return nil, fmt.Errorf("line %d: %w", lineNum, err)
				}
			case "notransaction":
				p.NoTransaction = true
			case "driver":
				if sec == secNone {
					return nil, fmt.Errorf("line %d: driver directive outside Up/Down", lineNum)
				}
				if strings.TrimSpace(buf.String()) != "" {
					return nil, fmt.Errorf("line %d: unterminated statement before driver directive", lineNum)
				}
				// A blank driver ("-- +godwit driver:") means no restriction.
				if val == "" {
					val = driverAll
				}
				if val != driverAll {
					val = normalize(val) // postgresql -> postgres, ...
					if !slices.Contains(drivers, val) {
						return nil, fmt.Errorf("line %d: unknown driver %q (available: %s)",
							lineNum, val, strings.Join(append([]string{driverAll}, drivers...), ", "))
					}
				}
				driver = val
			default:
				return nil, fmt.Errorf("line %d: unknown directive %q", lineNum, key)
			}
			continue
		}

		if sec == secNone {
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			return nil, fmt.Errorf("line %d: SQL before -- +godwit Up", lineNum)
		}

		if block {
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}

		// Outside a block: skip pure comment lines, split on trailing ';'.
		if strings.HasPrefix(trimmed, "--") && buf.Len() == 0 {
			continue
		}
		if trimmed == "" && buf.Len() == 0 {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			if err := flush(); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if block {
		return nil, fmt.Errorf("unterminated StatementBegin block")
	}
	if group != 0 {
		return nil, fmt.Errorf("unterminated RepeatStart block")
	}
	if strings.TrimSpace(buf.String()) != "" {
		return nil, fmt.Errorf("final statement is missing a terminating ';'")
	}
	if sec == secNone {
		return nil, fmt.Errorf("no -- +godwit Up section found")
	}
	return p, nil
}
