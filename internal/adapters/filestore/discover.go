package filestore

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ServiceInfo is one service listed by a workspace root.
type ServiceInfo struct {
	// Dir is the service directory relative to the workspace root, with "/"
	// separators, as written in the root's config.
	Dir string
	// Name is the service name: the "service" field of the service's own
	// config, or else the last element of Dir. It is not validated here.
	Name string
	// Configured is false when the service directory has no config.json yet.
	Configured bool
}

// Layout is where godwit runs: a plain project, or a workspace of services
// and possibly one service of it.
type Layout struct {
	// Root is the directory whose .godwit holds the project, or the workspace
	// root with the global targets. Runs and plugin binaries live under it.
	Root string
	// Services lists the workspace's services; nil for a plain project.
	Services []ServiceInfo
	// Current is the service the working directory is, or "" at the root of a
	// workspace and in a plain project.
	Current string
	// Warnings describe service entries that were skipped.
	Warnings []string
}

// IsWorkspace reports whether the layout is a workspace of services.
func (l Layout) IsWorkspace() bool { return l.Services != nil }

// Dir returns the absolute directory of a service of the layout.
func (l Layout) Dir(s ServiceInfo) string { return filepath.Join(l.Root, filepath.FromSlash(s.Dir)) }

// Discover finds the layout for the working directory cwd (absolute):
//
//   - cwd lists services in its config: cwd is a workspace root.
//   - an ancestor lists cwd, exactly, as one of its services: that ancestor is
//     the workspace root and cwd its current service.
//   - otherwise cwd is a plain project, whether or not it has a .godwit.
func Discover(cwd string) (Layout, error) {
	cwd = filepath.Clean(cwd)
	c, _, err := readConfig(cwd)
	if err != nil {
		return Layout{}, err
	}
	if len(c.Services) > 0 {
		return workspace(cwd, c.Services, "")
	}
	for dir := filepath.Dir(cwd); ; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(dir, cwd)
		if err == nil {
			rc, _, err := readConfig(dir)
			if err != nil {
				// An unrelated, broken config in an ancestor is not ours to
				// report; keep looking further up.
				rc = configJSON{}
			}
			rel = filepath.ToSlash(rel)
			for _, entry := range rc.Services {
				if clean, ok := cleanServiceDir(entry); ok && clean == rel {
					return workspace(dir, rc.Services, rel)
				}
			}
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return Layout{Root: cwd}, nil
}

// workspace builds the layout of the workspace rooted at root. current is the
// slash-separated directory of the current service, or "".
func workspace(root string, entries []string, current string) (Layout, error) {
	l := Layout{Root: root, Services: []ServiceInfo{}}
	for _, entry := range entries {
		dir, ok := cleanServiceDir(entry)
		if !ok {
			l.Warnings = append(l.Warnings, fmt.Sprintf("service directory %q skipped: it must be relative to the workspace root and inside it", entry))
			continue
		}
		si := ServiceInfo{Dir: dir, Name: path.Base(dir)}
		c, found, err := readConfig(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return Layout{}, err
		}
		si.Configured = found
		if c.Service != "" {
			si.Name = c.Service
		}
		if dir == current {
			l.Current = si.Name
		}
		l.Services = append(l.Services, si)
	}
	return l, nil
}

// cleanServiceDir normalises a service directory entry to a clean, relative,
// slash-separated path. ok is false for an absolute path, one that leaves the
// workspace root, or the root itself.
func cleanServiceDir(entry string) (string, bool) {
	entry = strings.ReplaceAll(strings.TrimSpace(entry), `\`, "/")
	if entry == "" || path.IsAbs(entry) || filepath.IsAbs(entry) || filepath.VolumeName(entry) != "" {
		return "", false
	}
	dir := path.Clean(entry)
	if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		return "", false
	}
	return dir, true
}

// CleanServiceDir is cleanServiceDir for callers that register a service.
func CleanServiceDir(entry string) (string, bool) { return cleanServiceDir(entry) }
