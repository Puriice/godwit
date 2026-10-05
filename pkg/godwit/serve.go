package godwit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Serve runs the plugin protocol on standard input and output until godwit
// closes the input. It returns nil on a clean shutdown.
//
// Standard output is reserved for the protocol, so Serve points os.Stdout at
// os.Stderr for as long as it runs.
func Serve(d Driver) error {
	out := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = out }()
	return ServeIO(os.Stdin, out, d)
}

// ServeIO is Serve over arbitrary streams, for tests.
func ServeIO(r io.Reader, w io.Writer, d Driver) error {
	s := &server{d: d, out: json.NewEncoder(w)}
	defer s.shutdown()

	in := bufio.NewReader(r) // no line length limit
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if werr := s.handleLine(line); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type server struct {
	d      Driver
	out    *json.Encoder
	conn   Connection
	unlock func() error // non-nil while the lock is held
}

type request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// params is the union of every method's parameters.
type params struct {
	ConnString string     `json:"connstring"`
	Password   string     `json:"password"`
	Target     wireTarget `json:"target"`
	Version    int64      `json:"version"`
	Migration  struct {
		Version       int64    `json:"version"`
		Name          string   `json:"name"`
		Checksum      string   `json:"checksum"`
		NoTransaction bool     `json:"noTransaction"`
		Statements    []string `json:"statements"`
		Batch         int64    `json:"batch"`
	} `json:"migration"`
}

type wireTarget struct {
	Name     string            `json:"name,omitempty"`
	Driver   string            `json:"driver"`
	Host     string            `json:"host,omitempty"`
	Port     int               `json:"port,omitempty"`
	Database string            `json:"database,omitempty"`
	User     string            `json:"user,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
}

type wireRecord struct {
	Version    int64     `json:"version"`
	Name       string    `json:"name"`
	Checksum   string    `json:"checksum"`
	AppliedAt  time.Time `json:"appliedAt"`
	DurationMS int64     `json:"durationMs"`
	Dirty      bool      `json:"dirty"`
	Batch      int64     `json:"batch,omitempty"`
}

func (s *server) handleLine(line []byte) error {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return s.out.Encode(response{Error: "invalid request: " + err.Error()})
	}
	result, err := s.safeDispatch(req)
	resp := response{ID: req.ID, Result: result}
	if err != nil {
		resp.Result, resp.Error = nil, err.Error()
	}
	return s.out.Encode(resp)
}

// safeDispatch turns a panic in driver code into an error response, so one bad
// call does not take the process (and its lock) down silently.
func (s *server) safeDispatch(req request) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("%s: internal error: %v", req.Method, r)
		}
	}()
	return s.dispatch(req)
}

func (s *server) dispatch(req request) (any, error) {
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("%s: bad params: %w", req.Method, err)
		}
	}
	ctx := context.Background() // godwit cancels by killing the process

	switch req.Method {
	case "handshake":
		i := s.d.Info()
		return map[string]any{
			"protocol": ProtocolVersion,
			"driver":   strings.ToLower(i.Name),
			"aliases":  i.Aliases,
			"schemes":  i.Schemes,
			"noHost":   i.NoHost,
		}, nil

	case "parse_connection":
		t, pw, err := s.d.ParseConnection(p.ConnString)
		if err != nil {
			return nil, err
		}
		return map[string]any{"target": toWire(t), "password": pw}, nil

	case "open":
		if s.conn != nil {
			return nil, errors.New("already open")
		}
		c, err := s.d.Open(ctx, fromWire(p.Target), p.Password)
		if err != nil {
			return nil, err
		}
		s.conn = c
		return struct{}{}, nil

	case "close":
		return struct{}{}, s.release()
	}

	// Everything below needs an open connection.
	if s.conn == nil {
		return nil, fmt.Errorf("%s: not open", req.Method)
	}
	switch req.Method {
	case "ensure_table":
		return struct{}{}, s.conn.EnsureTable(ctx)
	case "lock":
		if s.unlock != nil {
			return nil, errors.New("already locked")
		}
		unlock, err := s.conn.Lock(ctx)
		if err != nil {
			return nil, err
		}
		s.unlock = unlock
		return struct{}{}, nil
	case "unlock":
		if s.unlock == nil {
			return nil, errors.New("not locked")
		}
		unlock := s.unlock
		s.unlock = nil
		return struct{}{}, unlock()
	case "applied":
		recs, err := s.conn.Applied(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]wireRecord, 0, len(recs))
		for _, r := range recs {
			out = append(out, wireRecord(r))
		}
		return map[string]any{"records": out}, nil
	case "apply", "revert":
		m := Migration{
			Version: p.Migration.Version, Name: p.Migration.Name, Checksum: p.Migration.Checksum,
			NoTransaction: p.Migration.NoTransaction, Statements: p.Migration.Statements,
			Batch: p.Migration.Batch,
		}
		if req.Method == "apply" {
			return struct{}{}, s.conn.Apply(ctx, m)
		}
		return struct{}{}, s.conn.Revert(ctx, m)
	case "clear_dirty":
		return struct{}{}, s.conn.ClearDirty(ctx, p.Version)
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

// release drops the lock and closes the connection, if any.
func (s *server) release() error {
	var err error
	if s.unlock != nil {
		err = s.unlock()
		s.unlock = nil
	}
	if s.conn != nil {
		err = errors.Join(err, s.conn.Close())
		s.conn = nil
	}
	return err
}

func (s *server) shutdown() { s.release() } //nolint:errcheck // nowhere left to report it

func toWire(t Target) wireTarget { return wireTarget(t) }

func fromWire(w wireTarget) Target { return Target(w) }
