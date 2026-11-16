package cli

import (
	"errors"
	"fmt"
	"io"
	"path"
	"text/tabwriter"

	"github.com/puriice/godwit/internal/app"
)

// ServiceUsage documents the service subcommands.
const ServiceUsage = `usage: godwit service list
       godwit service add <dir> [name]
       godwit service remove <name>

  list    show the services of this workspace
  add     make <dir> (relative to the workspace root) a service: create or update
          <dir>/.godwit/config.json with the service name and list the directory
          in the root's .godwit/config.json. The name defaults to the last part of
          <dir>; it may hold lower case letters, digits and _, starting with a letter
  remove  take a service out of the workspace; its directory, configuration and
          databases are left alone

A workspace is a directory whose .godwit/config.json lists services. Its own
targets are global targets: every service can migrate on them, which suits a
development database the services share. Each service has its own migrations,
its own targets, and its own state table, godwit_migration_<name>, so services
sharing a database keep separate histories. Run godwit at the workspace root to
work on every service, or inside a service directory to work on that one.
`

// ServiceOps is what "godwit service" needs from the composition root.
type ServiceOps struct {
	// NewStore returns the configuration store of the service in dir, which is
	// relative to the workspace root with "/" separators.
	NewStore func(dir, name string) app.ProjectStore
	// CleanDir normalises a service directory; ok is false for one outside
	// the workspace root.
	CleanDir func(dir string) (string, bool)
}

// Service runs "godwit service ...". args excludes the leading "service".
func Service(svc *app.Service, ops ServiceOps, args []string, out io.Writer) error {
	if len(args) > 0 {
		rest := args[1:]
		switch args[0] {
		case "list":
			return serviceList(svc, rest, out)
		case "add":
			return serviceAdd(svc, ops, rest, out)
		case "remove":
			return serviceRemove(svc, rest, out)
		}
	}
	return errors.New("unknown service command\n\n" + ServiceUsage)
}

func serviceList(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("service list takes no arguments\n\n%s", ServiceUsage)
	}
	if !svc.IsWorkspace() {
		fmt.Fprintln(out, "This is a plain project, not a workspace. Make a directory a service with: godwit service add <dir>")
		return nil
	}
	names := svc.Services()
	if len(names) == 0 {
		fmt.Fprintln(out, "No services. Add one with: godwit service add <dir>")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, n := range names {
		dir, _ := svc.ServiceDir(n)
		own, global := 0, 0
		for _, t := range svc.ScopeTargets(n) {
			if svc.IsGlobal(refOf(n, t.Name)) {
				global++
			} else {
				own++
			}
		}
		current := ""
		if n == svc.CurrentService() {
			current = "(current)"
		}
		fmt.Fprintf(w, "%s\t%s\t%d target(s) + %d global\t%s\t%s\n", n, dir, own, global, svc.MigrationsLocation(n), current)
	}
	return w.Flush()
}

func serviceAdd(svc *app.Service, ops ServiceOps, args []string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("service add takes a directory and an optional name\n\n%s", ServiceUsage)
	}
	if svc.CurrentService() != "" {
		return errors.New("run godwit service add at the workspace root, not inside a service")
	}
	dir, ok := ops.CleanDir(args[0])
	if !ok {
		return fmt.Errorf("%q must be a directory inside the workspace root, given relative to it", args[0])
	}
	name := path.Base(dir)
	if len(args) == 2 {
		name = args[1]
	}
	wasWorkspace := svc.IsWorkspace()
	globals := len(svc.ScopeTargets(""))
	orphans, rootMigrations := 0, ""
	if !wasWorkspace {
		files, _ := svc.MigrationFiles("")
		orphans, rootMigrations = len(files), svc.MigrationsLocation("")
	}
	if err := svc.AddService(name, dir, ops.NewStore(dir, name)); err != nil {
		return err
	}
	if err := svc.EnsureMigrationsDir(name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added service %s in %s (migrations: %s, state table: godwit_migration_%s)\n", name, dir, svc.MigrationsLocation(name), name)
	if !wasWorkspace && globals > 0 {
		fmt.Fprintf(out, "This project is now a workspace: its %d target(s) are global targets that every service can use.\n", globals)
	}
	if orphans > 0 {
		fmt.Fprintf(out, "Note: the workspace root has no migrations of its own; move the %d migration file(s) in %s into a service.\n", orphans, rootMigrations)
	}
	return nil
}

func serviceRemove(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("service remove takes 1 argument (name)\n\n%s", ServiceUsage)
	}
	dir, ok := svc.ServiceDir(args[0])
	if !ok {
		return fmt.Errorf("no service named %q", args[0])
	}
	if err := svc.RemoveService(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed service %s from the workspace. %s and its databases were not touched.\n", args[0], dir)
	return nil
}
