// Package plugin is the driven adapter that lets users add database drivers
// godwit does not ship. A plugin is an external executable that talks to
// godwit over its stdin and stdout, so it works on every OS and can be written
// in any language. See docs/plugins.md for the protocol.
package plugin

import (
	"encoding/json"
	"time"
)

// ProtocolVersion is the plugin protocol version this build speaks.
const ProtocolVersion = 1

// One JSON object per line in each direction. godwit sends requests and the
// plugin answers each with exactly one response carrying the same id.
type request struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// handshakeResult is the plugin's answer to "handshake".
type handshakeResult struct {
	Protocol int      `json:"protocol"`
	Driver   string   `json:"driver"`
	Aliases  []string `json:"aliases,omitempty"`
	Schemes  []string `json:"schemes,omitempty"`
	// NoHost marks drivers without a network endpoint (file databases).
	NoHost bool `json:"noHost,omitempty"`
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

type parseParams struct {
	ConnString string `json:"connstring"`
}

type parseResult struct {
	Target   wireTarget `json:"target"`
	Password string     `json:"password,omitempty"`
}

type openParams struct {
	Target   wireTarget `json:"target"`
	Password string     `json:"password,omitempty"`
}

type wireMigration struct {
	Version       int64    `json:"version"`
	Name          string   `json:"name"`
	Checksum      string   `json:"checksum"`
	NoTransaction bool     `json:"noTransaction,omitempty"`
	Statements    []string `json:"statements"`
	// Batch is the apply run this migration belongs to (0 when not applying).
	Batch int64 `json:"batch,omitempty"`
}

type migrationParams struct {
	Migration wireMigration `json:"migration"`
}

type clearDirtyParams struct {
	Version int64 `json:"version"`
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

type appliedResult struct {
	Records []wireRecord `json:"records"`
}
