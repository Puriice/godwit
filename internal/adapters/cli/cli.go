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

// Probe validates a plugin before it is saved and describes its driver.
type Probe func(domain.PluginSpec) (domain.DriverInfo, error)

// PluginUsage documents the plugin subcommands.
const PluginUsage = `usage: godwit plugin list
       godwit plugin add <name> <command> [args...]
       godwit plugin remove <name>

  add      register a driver plugin: an executable that speaks godwit's plugin
           protocol (docs/plugins.md). <command> is a path relative to the
           project, or a name found in .godwit/plugins or on PATH. The plugin is
           started once to check it before it is saved.
  remove   unregister a plugin (targets using its driver are left alone)
  list     show registered plugins

Plugins run with your privileges: only add executables you trust.
`

// Plugin runs "godwit plugin ...". args excludes the leading "plugin".
func Plugin(svc *app.Service, probe Probe, args []string, out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			if len(args) != 1 {
				break
			}
			plugins := svc.Plugins()
			if len(plugins) == 0 {
				fmt.Fprintln(out, "No plugins. Add one with: godwit plugin add <name> <command>")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, p := range plugins {
				fmt.Fprintf(w, "%s\t%s\n", p.Name, strings.TrimSpace(p.Command+" "+strings.Join(p.Args, " ")))
			}
			return w.Flush()
		case "add":
			if len(args) < 3 {
				break
			}
			spec := domain.PluginSpec{Name: args[1], Command: args[2], Args: args[3:]}
			info, err := probe(spec)
			if err != nil {
				return err
			}
			if !strings.EqualFold(info.Name, strings.TrimSpace(spec.Name)) {
				return fmt.Errorf("plugin provides driver %q, not %q; use that name", info.Name, spec.Name)
			}
			if err := svc.AddPlugin(spec); err != nil {
				return err
			}
			fmt.Fprintf(out, "Added plugin %q (driver %s). Restart godwit to use it.\n", spec.Name, info.Name)
			return nil
		case "remove":
			if len(args) != 2 {
				break
			}
			if err := svc.RemovePlugin(args[1]); err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed plugin %q.\n", args[1])
			return nil
		}
	}
	return errors.New("unknown plugin command\n\n" + PluginUsage)
}

// AuthUsage documents the auth subcommands.
const AuthUsage = `usage: godwit auth list
       godwit auth add <name> <driver> <connection string>
       godwit auth remove <name>
       godwit auth enable <name>
       godwit auth disable <name>

  list               show every target; passwords are never printed
  remove             delete a target and its saved password (databases are untouched)
  disable / enable   temporarily skip a target, or use it again (settings and password are kept)

  driver             postgres | mysql | any driver added with "godwit plugin add"
  connection string  postgres://user:pass@host:5432/db?sslmode=disable
                     mysql://user:pass@host:3306/db
                     user:pass@tcp(host:3306)/db        (mysql only)
                     plugin drivers define their own syntax

Quote the connection string. The password is saved to .godwit/.env and
everything else to .godwit/config.json.
`

// Auth runs "godwit auth ...". args excludes the leading "auth".
func Auth(svc *app.Service, args []string, out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return authAdd(svc, args[1:], out)
		case "remove":
			return authRemove(svc, args[1:], out)
		case "enable":
			return setEnabled(svc, "enable", true, args[1:], out)
		case "disable":
			return setEnabled(svc, "disable", false, args[1:], out)
		case "list":
			return authList(svc, args[1:], out)
		}
	}
	return errors.New("unknown auth command\n\n" + AuthUsage)
}

func authList(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("auth list takes no arguments\n\n%s", AuthUsage)
	}
	targets := svc.Targets()
	if len(targets) == 0 {
		fmt.Fprintln(out, "No targets. Add one with: godwit auth add <name> <driver> <connection string>")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, t := range targets {
		has := svc.HasPassword(t.Name)
		var notes []string
		if t.Disabled {
			notes = append(notes, "disabled")
		}
		if !has && svc.NeedsPassword(t.Name) {
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

func authAdd(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 3 {
		return fmt.Errorf("auth add takes 3 arguments (name, driver, connection string), got %d\n\n%s", len(args), AuthUsage)
	}
	name, driver, conn := args[0], args[1], args[2]

	t, password, err := svc.ParseConnectionString(driver, conn)
	if err != nil {
		return err
	}
	t.Name = name
	if err := svc.AddTarget(t, password, true); err != nil {
		return err
	}

	fmt.Fprintf(out, "Added target %q (%s)\n", t.Name, t.RedactedString(false))
	if password == "" {
		if svc.NeedsPassword(t.Name) {
			fmt.Fprintln(out, "No password in the connection string; godwit will ask for it when it is needed.")
		}
	} else {
		fmt.Fprintln(out, "Password saved to .godwit/.env (git-ignored).")
	}
	return nil
}

func authRemove(svc *app.Service, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("auth remove takes 1 argument (name), got %d\n\n%s", len(args), AuthUsage)
	}
	name := args[0]
	if err := svc.RemoveTarget(name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed target %q and its saved password. The database itself was not touched.\n", name)
	return nil
}

// setEnabled implements "auth enable" and "auth disable".
func setEnabled(svc *app.Service, cmd string, enabled bool, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: godwit auth %s <name>", cmd)
	}
	name := args[0]
	t, ok := svc.Target(name)
	if !ok {
		return fmt.Errorf("no target named %q", name)
	}
	if t.Disabled == !enabled {
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		fmt.Fprintf(out, "Target %q is already %s.\n", name, state)
		return nil
	}
	if err := svc.SetTargetEnabled(name, enabled); err != nil {
		return err
	}
	if enabled {
		fmt.Fprintf(out, "Enabled target %q.\n", name)
	} else {
		fmt.Fprintf(out, "Disabled target %q. Its settings and password are kept; run `godwit auth enable %s` to use it again.\n", name, name)
	}
	return nil
}
