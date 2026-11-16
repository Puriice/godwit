package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultStateTable is the state table of a plain project. Every service of a
// workspace has its own, named by StateTable, so that services sharing one
// database keep separate histories.
const DefaultStateTable = "godwit_migration"

// MaxServiceName is the longest service name allowed. The name becomes part
// of a table name, and engines cap identifiers (PostgreSQL at 63 bytes).
const MaxServiceName = 40

var serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateServiceName checks that name is usable as a service name: lower
// case letters, digits and underscores, starting with a letter. The name is
// part of a SQL identifier and of environment variable names.
func ValidateServiceName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("service name is required")
	case len(name) > MaxServiceName:
		return fmt.Errorf("service name %q is too long (at most %d characters)", name, MaxServiceName)
	case !serviceNameRe.MatchString(name):
		return fmt.Errorf("invalid service name %q: use lower case letters, digits and underscores, starting with a letter", name)
	}
	return nil
}

// StateTable returns the name of the state table a service records its
// migrations in: DefaultStateTable for a plain project (service ""), and
// DefaultStateTable_<service> otherwise.
func StateTable(service string) string {
	if service == "" {
		return DefaultStateTable
	}
	return DefaultStateTable + "_" + service
}

// TargetRef names a target in the context of a service: the service's
// migrations and state table are used with the target's connection. The
// target itself may be defined by the service or be a global target of the
// workspace. Service is empty in a plain project.
type TargetRef struct {
	Service string
	Target  string
}

// String returns "service/target", or just the target without a service.
func (r TargetRef) String() string {
	if r.Service == "" {
		return r.Target
	}
	return r.Service + "/" + r.Target
}

// ParseTargetRef reverses String: "users/dev" names target dev of service
// users, and "dev" a target with no service.
func ParseTargetRef(s string) TargetRef {
	if svc, t, ok := strings.Cut(s, "/"); ok {
		return TargetRef{Service: svc, Target: t}
	}
	return TargetRef{Target: s}
}
