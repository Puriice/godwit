// Package fsmigrations is the driven adapter that reads migrations from .sql
// files on disk, in godwit's goose-style annotated format.
package fsmigrations

import (
	"bufio"
	"fmt"
	"slices"
	"strings"

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

// Parse reads a migration file's contents. drivers lists the driver names a
// "driver:" directive may use (besides "all"); anything else is an error, so a
// typo cannot silently make statements run nowhere.
func Parse(src string, drivers []string) (*Parsed, error) {
	p := &Parsed{}
	var (
		sec     = secNone
		driver  = driverAll
		block   bool // inside StatementBegin/End
		buf     strings.Builder
		lineNum int
	)

	flush := func() {
		sql := strings.TrimSpace(buf.String())
		buf.Reset()
		if sql == "" {
			return
		}
		st := Statement{Driver: driver, SQL: sql}
		if sec == secUp {
			p.Up = append(p.Up, st)
		} else {
			p.Down = append(p.Down, st)
		}
	}

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, prefix) {
			if block && !strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), "StatementEnd") {
				return nil, fmt.Errorf("line %d: directive inside StatementBegin block", lineNum)
			}
			d := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			// Drop trailing comment, e.g. "driver: all -- default".
			if i := strings.Index(d, "--"); i >= 0 {
				d = strings.TrimSpace(d[:i])
			}
			key, val, _ := strings.Cut(d, ":")
			key = strings.ToLower(strings.TrimSpace(key))
			val = strings.ToLower(strings.TrimSpace(val))
			// Allow "driver mysql" as well as "driver: mysql".
			if k, v, ok := strings.Cut(key, " "); ok && k == "driver" {
				key, val = k, strings.TrimSpace(v)
			}

			switch key {
			case "up", "down":
				if buf.Len() > 0 && strings.TrimSpace(buf.String()) != "" {
					return nil, fmt.Errorf("line %d: unterminated statement before %s", lineNum, key)
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
				flush()
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
					val = domain.NormalizeDriver(val) // postgresql -> postgres, ...
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
			flush()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if block {
		return nil, fmt.Errorf("unterminated StatementBegin block")
	}
	if strings.TrimSpace(buf.String()) != "" {
		return nil, fmt.Errorf("final statement is missing a terminating ';'")
	}
	if sec == secNone {
		return nil, fmt.Errorf("no -- +godwit Up section found")
	}
	return p, nil
}
