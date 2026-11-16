package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// ServiceFlagUsage documents -s/--service for every command group that has it.
const ServiceFlagUsage = `In a workspace of services (see "godwit service"), -s NAME or --service NAME
limits a command to one service, and can be repeated. Without it, a command
acts on every service at the workspace root, and on the current service inside
a service directory. A target can also be named service/target, as in users/dev.
`

// serviceFlag is the repeatable -s/--service flag.
type serviceFlag []string

func (f *serviceFlag) String() string { return strings.Join(*f, ",") }

func (f *serviceFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("empty service name")
	}
	if !slices.Contains(*f, v) {
		*f = append(*f, v)
	}
	return nil
}

func (f *serviceFlag) define(fs *flag.FlagSet) {
	fs.Var(f, "s", "")
	fs.Var(f, "service", "")
}

// parseCmdFlags parses a subcommand's flags with the standard flag package:
// flags come before the positional arguments, which it returns.
func parseCmdFlags(cmd, usage string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	define(fs)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%s: %v\n\n%s", cmd, err, usage)
	}
	return fs.Args(), nil
}

// scopes returns the services a command acts on: those named with -s, or the
// default ones. A plain project has one scope, "".
func scopes(svc *app.Service, services serviceFlag) ([]string, error) {
	if len(services) == 0 {
		return svc.DefaultServices(), nil
	}
	if !svc.IsWorkspace() {
		return nil, errors.New("-s/--service: this is not a workspace (no \"services\" in .godwit/config.json)")
	}
	known := svc.Services()
	for _, s := range services {
		if !slices.Contains(known, s) {
			return nil, fmt.Errorf("no service named %q (services: %s)", s, strings.Join(known, ", "))
		}
	}
	return services, nil
}

// oneScope is the service a command that works on one service uses: the one
// named with -s, else the current service, else "" (the plain project or the
// workspace root).
func oneScope(svc *app.Service, services serviceFlag) (string, error) {
	switch {
	case len(services) > 1:
		return "", errors.New("name a single service with -s")
	case len(services) == 1:
		s, err := scopes(svc, services)
		if err != nil {
			return "", err
		}
		return s[0], nil
	}
	return svc.CurrentService(), nil
}

// selectRefs resolves target names in the chosen services. A name is looked
// up in each service; without -s, a service that lacks it is skipped, but the
// name must exist somewhere. A "service/target" name is taken as it is.
// Without names, every enabled target of each service is used.
func selectRefs(svc *app.Service, services serviceFlag, names []string) ([]domain.TargetRef, error) {
	scs, err := scopes(svc, services)
	if err != nil {
		return nil, err
	}
	var out []domain.TargetRef
	add := func(r domain.TargetRef) {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if len(names) == 0 {
		for _, sc := range scs {
			for _, t := range svc.ScopeTargets(sc) {
				if !t.Disabled {
					add(domain.TargetRef{Service: sc, Target: t.Name})
				}
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no enabled targets; add one with: godwit auth add <name> <driver> <connection string>")
		}
		return out, nil
	}
	for _, n := range names {
		if strings.Contains(n, "/") && svc.IsWorkspace() {
			r := domain.ParseTargetRef(n)
			if _, ok := svc.Target(r); !ok || r.Service == "" {
				return nil, fmt.Errorf("no target named %q", n)
			}
			add(r)
			continue
		}
		found := false
		for _, sc := range scs {
			r := domain.TargetRef{Service: sc, Target: n}
			if _, ok := svc.Target(r); ok {
				add(r)
				found = true
			} else if len(services) > 0 {
				return nil, fmt.Errorf("no target named %q for service %s", n, sc)
			}
		}
		if !found {
			return nil, fmt.Errorf("no target named %q", n)
		}
	}
	return out, nil
}

// selectRef is selectRefs for commands that work on exactly one target.
func selectRef(svc *app.Service, services serviceFlag, name string) (domain.TargetRef, error) {
	refs, err := selectRefs(svc, services, []string{name})
	if err != nil {
		return domain.TargetRef{}, err
	}
	if len(refs) > 1 {
		var all []string
		for _, r := range refs {
			all = append(all, r.String())
		}
		return domain.TargetRef{}, fmt.Errorf("%q is used by several services (%s); name one with -s or as service/target", name, strings.Join(all, ", "))
	}
	return refs[0], nil
}

// eachTarget runs fn on every target and reports all failures together.
func eachTarget(refs []domain.TargetRef, out io.Writer, fn func(ref domain.TargetRef) error) error {
	var failed []string
	for _, r := range refs {
		fmt.Fprintf(out, "== %s ==\n", r)
		if err := fn(r); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			failed = append(failed, r.String())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed on %d target(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// refServices lists the services of refs, in order of first appearance.
func refServices(refs []domain.TargetRef) []string {
	var out []string
	for _, r := range refs {
		if !slices.Contains(out, r.Service) {
			out = append(out, r.Service)
		}
	}
	return out
}

// filesHeading is the heading over a service's migration files.
func filesHeading(svc *app.Service, service string) string {
	if service == "" {
		return fmt.Sprintf("== migration files (%s) ==", svc.MigrationsLocation(service))
	}
	return fmt.Sprintf("== %s: migration files (%s) ==", service, svc.MigrationsLocation(service))
}

func refOf(service, target string) domain.TargetRef {
	return domain.TargetRef{Service: service, Target: target}
}
