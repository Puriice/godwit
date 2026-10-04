package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// builtin knows only the built-in drivers; use a Registry to include plugins.
var builtin = NewRegistry()

// NormalizeDriver maps built-in driver aliases onto godwit's driver names.
func NormalizeDriver(name string) string { return builtin.Normalize(name) }

// mysqlDSN matches go-sql-driver style DSNs: user:pass@tcp(host:port)/db?params
var mysqlDSN = regexp.MustCompile(`^([^:@/]*)(?::(.*))?@(\w+)\((.*?)\)/([^?]*)(?:\?(.*))?$`)

// ParseConnectionString turns a connection string into a Target (without a
// name) and its password. Accepted forms:
//
//	postgres: postgres://user:pass@host:5432/db?sslmode=disable
//	mysql:    mysql://user:pass@host:3306/db   or   user:pass@tcp(host:3306)/db
//
// The password is returned separately so callers never store it in a Target.
func ParseConnectionString(driver, conn string) (t Target, password string, err error) {
	return builtin.ParseConnectionString(driver, conn)
}

func parseURL(t *Target, schemes []string, conn string) (string, error) {
	u, err := url.Parse(conn)
	if err != nil {
		// url.Error echoes the whole input, which includes the password.
		return "", fmt.Errorf("invalid connection URL")
	}
	valid := false
	for _, s := range schemes {
		valid = valid || strings.EqualFold(u.Scheme, s)
	}
	if !valid {
		return "", fmt.Errorf("scheme %q does not match driver %q", u.Scheme, t.Driver)
	}
	t.Host = u.Hostname()
	if p := u.Port(); p != "" {
		if t.Port, err = parsePort(p); err != nil {
			return "", err
		}
	}
	t.Database = strings.TrimPrefix(u.Path, "/")
	var password string
	if u.User != nil {
		t.User = u.User.Username()
		password, _ = u.User.Password()
	}
	t.Params = firstValues(u.Query())
	return password, nil
}

func parseMySQLDSN(t *Target, conn string) (string, error) {
	m := mysqlDSN.FindStringSubmatch(conn)
	if m == nil {
		return "", fmt.Errorf("expected user:pass@tcp(host:port)/database or mysql://user:pass@host:port/database")
	}
	user, password, network, addr, db, query := m[1], m[2], m[3], m[4], m[5], m[6]
	if network != "tcp" {
		return "", fmt.Errorf("unsupported network %q (only tcp)", network)
	}
	host, port := addr, ""
	if i := strings.LastIndex(addr, ":"); i >= 0 && !strings.HasSuffix(addr, "]") {
		host, port = addr[:i], addr[i+1:]
	}
	t.Host, t.User, t.Database = strings.Trim(host, "[]"), user, db
	if port != "" {
		var err error
		if t.Port, err = parsePort(port); err != nil {
			return "", err
		}
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("invalid DSN parameters")
	}
	t.Params = firstValues(q)
	return password, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return n, nil
}

func firstValues(q url.Values) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for k, v := range q {
		out[k] = v[0]
	}
	return out
}

// RedactedString renders the target as a connection URL that is safe to
// print: the password is shown as **** when one is set, and never otherwise.
func (t Target) RedactedString(hasPassword bool) string {
	var b strings.Builder
	b.WriteString(t.Driver)
	b.WriteString("://")
	b.WriteString(url.PathEscape(t.User))
	if hasPassword {
		b.WriteString(":****")
	}
	if t.User != "" || hasPassword {
		b.WriteByte('@')
	}
	b.WriteString(t.Host)
	if t.Port != 0 {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(t.Port))
	}
	b.WriteByte('/')
	b.WriteString(url.PathEscape(t.Database))
	if len(t.Params) > 0 {
		q := make(url.Values, len(t.Params))
		for k, v := range t.Params {
			q.Set(k, v)
		}
		b.WriteByte('?')
		b.WriteString(q.Encode()) // sorted by key
	}
	return b.String()
}
