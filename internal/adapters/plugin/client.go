package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// resolve finds a plugin executable. A command with a path separator is taken
// relative to the project root; a bare name is looked up in
// <root>/.godwit/plugins first, then on PATH. exec.LookPath honours PATHEXT on
// Windows and the executable bit elsewhere.
func resolve(root, command string) (string, error) {
	if strings.ContainsAny(command, `/\`) {
		p := filepath.FromSlash(command)
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		return exec.LookPath(p)
	}
	if p, err := exec.LookPath(filepath.Join(root, ".godwit", "plugins", command)); err == nil {
		return p, nil
	}
	return exec.LookPath(command)
}

// client is one running plugin process. Calls are serialized.
type client struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader

	mu   sync.Mutex
	next int64
	dead error
}

func start(name, path string, args []string, dir string) (*client, error) {
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr // plugin logs go straight to the user
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting plugin %s: %w", name, err)
	}
	return &client{name: name, cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout)}, nil
}

// call sends one request and decodes the result into result (may be nil).
// Cancelling ctx kills the plugin: a half-finished call cannot be resumed.
func (c *client) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return c.dead
	}
	if err := ctx.Err(); err != nil {
		return err // nothing was sent, so the plugin is still usable
	}
	c.next++
	id := c.next

	b, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			c.cmd.Process.Kill() //nolint:errcheck // already gone is fine
		case <-stop:
		}
	}()

	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return c.fail(ctx, err)
	}
	var resp response
	for {
		line, rerr := c.out.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if err := json.Unmarshal(line, &resp); err != nil {
				c.dead = fmt.Errorf("plugin %s: invalid response to %s: %w", c.name, method, err)
				return c.dead
			}
			break
		}
		if rerr != nil {
			return c.fail(ctx, rerr)
		}
	}
	if resp.ID != id {
		c.dead = fmt.Errorf("plugin %s: response id %d does not match request %d", c.name, resp.ID, id)
		return c.dead
	}
	if resp.Error != "" {
		return fmt.Errorf("plugin %s: %s", c.name, resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("plugin %s: bad %s result: %w", c.name, method, err)
		}
	}
	return nil
}

func (c *client) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		c.dead = ctx.Err()
	} else {
		c.dead = fmt.Errorf("plugin %s: connection lost (did it exit?): %w", c.name, err)
	}
	return c.dead
}

// close asks the plugin to exit by closing its stdin, then kills it if it
// lingers. Killing, not SIGTERM, keeps this identical on Windows.
func (c *client) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return exitErr(err)
	case <-time.After(3 * time.Second):
		c.cmd.Process.Kill() //nolint:errcheck
		<-done
		return nil
	}
}

func exitErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil // plugins may exit non-zero on shutdown; not our problem
	}
	return err
}
