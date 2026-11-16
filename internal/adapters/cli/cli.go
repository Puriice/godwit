// Package cli is a driving adapter for non-interactive commands.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// AuthUsage documents the auth subcommands.
const AuthUsage = `usage: godwit auth list [-s SERVICE]
       godwit auth add [-s SERVICE] <name> <driver> <connection string>
       godwit auth remove [-s SERVICE] <name>
       godwit auth enable [-s SERVICE] <name>
       godwit auth disable [-s SERVICE] <name>

  list               show every target; passwords are never printed
  remove             delete a target and its saved password (databases are untouched)
  disable / enable   temporarily skip a target, or use it again (settings and password are kept)

  driver             postgres | mysql | sqlite | any driver added with "godwit plugin add"
  connection string  postgres://user:pass@host:5432/db?sslmode=disable
                     mysql://user:pass@host:3306/db
                     user:pass@tcp(host:3306)/db        (mysql only)
                     sqlite://path/to/app.db                 (sqlite: no user or password)
                     plugin drivers define their own syntax

Quote the connection string. The password is saved to .godwit/.env and
everything else to .godwit/config.json.

In a workspace, a target added at the root (without -s) is a global target that
every service can use, such as a shared development database. A target added
with -s, or inside a service directory, belongs to that service and is kept in
its own .godwit; it hides a global target of the same name for that service.
`

// Auth runs "godwit auth ...". args excludes the leading "auth".
func Auth(svc *app.Service, args []string, out io.Writer) error {
	if len(args) > 0 {
		var services serviceFlag
		rest, err := parseCmdFlags("auth "+args[0], AuthUsage, args[1:], services.define)
		if err != nil {
			return err
		}
		switch args[0] {
		case "add":
			return authAdd(svc, services, rest, out)
		case "remove":
			return authRemove(svc, services, rest, out)
		case "enable":
			return setEnabled(svc, services, "enable", true, rest, out)
		case "disable":
			return setEnabled(svc, services, "disable", false, rest, out)
		case "list":
			return authList(svc, services, rest, out)
		}
	}
	return errors.New("unknown auth command\n\n" + AuthUsage)
}

func authList(svc *app.Service, services serviceFlag, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("auth list takes no arguments\n\n%s", AuthUsage)
	}
	if !svc.IsWorkspace() {
		targets := svc.ScopeTargets("")
		if len(targets) == 0 {
			fmt.Fprintln(out, "No targets. Add one with: godwit auth add <name> <driver> <connection string>")
			return nil
		}
		return listTargets(svc, "", out)
	}
	scs, err := scopes(svc, services)
	if err != nil {
		return err
	}
	if len(services) == 0 && svc.CurrentService() == "" {
		fmt.Fprintln(out, "== global ==")
		if len(svc.ScopeTargets("")) == 0 {
			fmt.Fprintln(out, "no global targets")
		} else if err := listTargets(svc, "", out); err != nil {
			return err
		}
	}
	for _, sc := range scs {
		fmt.Fprintf(out, "== %s ==\n", sc)
		if len(svc.ScopeTargets(sc)) == 0 {
			fmt.Fprintln(out, "no targets")
			continue
		}
		if err := listTargets(svc, sc, out); err != nil {
			return err
		}
	}
	return nil
}

// listTargets prints the targets of one service (or the plain project, or a
// workspace's global targets).
func listTargets(svc *app.Service, service string, out io.Writer) error {
	globals := map[string]bool{}
	if service != "" {
		for _, g := range svc.ScopeTargets("") {
			globals[g.Name] = true
		}
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, t := range svc.ScopeTargets(service) {
		ref := domain.TargetRef{Service: service, Target: t.Name}
		has := svc.HasPassword(ref)
		var notes []string
		switch {
		case service != "" && svc.IsGlobal(ref):
			notes = append(notes, "global")
		case globals[t.Name]:
			notes = append(notes, "shadows global")
		}
		if t.Disabled {
			notes = append(notes, "disabled")
		}
		if !has && svc.NeedsPassword(ref) {
			notes = append(notes, "no password")
		}
		note := ""
		if len(notes) > 0 {
			note = "(" + strings.Join(notes, ", ") + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.RedactedString(has), note)
	}
	return w.Flush()
}

// envFileOf says where a service's passwords are saved.
func envFileOf(svc *app.Service, service string) string {
	if dir, ok := svc.ServiceDir(service); ok {
		return dir + "/.godwit/.env"
	}
	return ".godwit/.env"
}

func authAdd(svc *app.Service, services serviceFlag, args []string, out io.Writer) error {
	if len(args) != 3 {
		return fmt.Errorf("auth add takes 3 arguments (name, driver, connection string), got %d\n\n%s", len(args), AuthUsage)
	}
	name, driver, conn := args[0], args[1], args[2]
	service, err := oneScope(svc, services)
	if err != nil {
		return err
	}

	t, password, err := svc.ParseConnectionString(driver, conn)
	if err != nil {
		return err
	}
	t.Name = name
	if err := svc.AddTarget(service, t, password, true); err != nil {
		return err
	}

	ref := domain.TargetRef{Service: service, Target: t.Name}
	switch {
	case service != "":
		fmt.Fprintf(out, "Added target %q to service %s (%s)\n", t.Name, service, t.RedactedString(false))
	case svc.IsWorkspace():
		fmt.Fprintf(out, "Added global target %q (%s); every service can use it\n", t.Name, t.RedactedString(false))
	default:
		fmt.Fprintf(out, "Added target %q (%s)\n", t.Name, t.RedactedString(false))
	}
	if password == "" {
		if svc.NeedsPassword(ref) {
			fmt.Fprintf(out, "No password in the connection string; godwit will ask for it when it is needed, or set %s.\n", svc.PasswordKey(ref))
		}
	} else {
		fmt.Fprintf(out, "Password saved to %s (git-ignored) as %s.\n", envFileOf(svc, service), svc.PasswordKey(ref))
	}
	return nil
}

// configRef is the target a configuration command names: in the service
// named with -s, else the current service, else the plain project or the
// workspace's global targets.
func configRef(svc *app.Service, services serviceFlag, name string) (domain.TargetRef, error) {
	if strings.Contains(name, "/") && svc.IsWorkspace() {
		return domain.ParseTargetRef(name), nil
	}
	service, err := oneScope(svc, services)
	if err != nil {
		return domain.TargetRef{}, err
	}
	return domain.TargetRef{Service: service, Target: name}, nil
}

func authRemove(svc *app.Service, services serviceFlag, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("auth remove takes 1 argument (name), got %d\n\n%s", len(args), AuthUsage)
	}
	ref, err := configRef(svc, services, args[0])
	if err != nil {
		return err
	}
	if err := svc.RemoveTarget(ref); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed target %q and its saved password. The database itself was not touched.\n", ref.String())
	return nil
}

// setEnabled implements "auth enable" and "auth disable".
func setEnabled(svc *app.Service, services serviceFlag, cmd string, enabled bool, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: godwit auth %s [-s SERVICE] <name>", cmd)
	}
	ref, err := configRef(svc, services, args[0])
	if err != nil {
		return err
	}
	t, ok := svc.Target(ref)
	if !ok {
		return fmt.Errorf("no target named %q", ref.String())
	}
	name := ref.String()
	if t.Disabled == !enabled {
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		fmt.Fprintf(out, "Target %q is already %s.\n", name, state)
		return nil
	}
	if err := svc.SetTargetEnabled(ref, enabled); err != nil {
		return err
	}
	shared := ""
	if ref.Service != "" && svc.IsGlobal(ref) {
		shared = " It is a global target, so this applies to every service."
	}
	if enabled {
		fmt.Fprintf(out, "Enabled target %q.%s\n", name, shared)
	} else {
		fmt.Fprintf(out, "Disabled target %q.%s Its settings and password are kept; run `%s` to use it again.\n", name, shared, svc.AuthCommand("enable", ref))
	}
	return nil
}
