# Driver plugins: how they work inside godwit

This is for people changing godwit itself. To write a plugin, read
[plugins.md](plugins.md).

## Where the pieces live

Dependencies point inward (`internal/architecture_test.go` enforces it), so the
plugin feature is split across the layers:

| Layer | File | Role |
|---|---|---|
| domain | `internal/domain/drivers.go` | `DriverInfo`, `Registry`, `PluginSpec`. Pure data and functions, no I/O. |
| domain | `internal/domain/connstring.go` | Built-in URL and MySQL DSN parsing, shared helpers. |
| app | `internal/app/drivers.go` | `DriverDescriber`, `Combine`, and the `Service` methods for drivers and plugins. |
| public | `pkg/godwit/` | The importable Go API for plugin authors: `Driver` and `Connection` interfaces plus `Serve`, which speaks the protocol. |
| adapter | `internal/adapters/plugin/` | Process management, the wire protocol, and `app.DatabaseFactory` / `app.Database` implementations. |
| adapter | `internal/adapters/filestore/config.go` | Persists `plugins` in `.godwit/config.json`. |
| adapter | `internal/adapters/cli/plugin.go` | `godwit plugin add/install/list/remove`. |
| root | `cmd/godwit/main.go` | Wires everything together. |

The `plugin` adapter may import only `domain` and `app`. It cannot see `sqldb`,
which is why merging the built-in and plugin factories is done in `app.Combine`
and the composition root, not inside an adapter.

## Startup flow

`main.go` does this on every run:

1. `filestore.Load()` reads the project to get `Plugins`.
2. `plugin.New(root, specs)` starts each plugin once, sends `handshake`, then
   stops it. Failures become `Warnings()`, printed to stderr, and that plugin is
   skipped.
3. `app.Combine(sqldb.NewFactory(), plugins)` forms the factory the service uses.
4. A `domain.Registry` is built from the built-ins plus `plugins.DriverInfos()`.
   Its `Normalize` is given to `fsmigrations` so `-- +godwit driver: <alias>`
   works for plugin drivers.
5. `app.New` builds its own registry the same way, from any
   `DriverDescriber` the factory exposes. `Combine` forwards the plugin
   factory's infos, so the service and the migration parser agree.

Every godwit command therefore starts each registered plugin once. Keep
plugins fast to start.

## Driver registry

`domain.Registry` answers three questions about a driver name:

- `Normalize(name)` maps aliases (`pg`, `mariadb`, a plugin's aliases) to the
  canonical name.
- `Info(name)` returns the `DriverInfo`.
- `ParseConnectionString(driver, conn)` parses a connection string.

For built-ins, `DriverInfo.Parse` is nil, so the registry uses generic URL
parsing against `Schemes` (plus the MySQL `user:pass@tcp(...)/db` form via an
unexported hook) and requires host, database and user.

For plugins, `Parse` is set. It starts the plugin, calls `parse_connection`,
and returns the result. The registry then trusts it: no host, database or user
checks. The plugin owns validation. The registry overwrites `Target.Driver` with
the canonical name, so a plugin cannot mislabel its own targets.

`DriverInfo.NoHost` drives three behaviours, via `Service.DriverNoHost` and
`Service.NeedsPassword`:

- the TUI and `godwit init` form do not require Host and User;
- no password is requested, in the TUI or on the CLI;
- `Service.open` does not fail for a missing password.

The package-level `domain.ParseConnectionString` and `NormalizeDriver` use a
registry holding only the built-ins. They exist for code that has no service,
such as the default migration parser and older tests.

## Process model

`adapters/plugin/client.go` wraps one running process.

- **One process per open database.** `Factory.Open` starts a fresh process,
  sends `open`, and returns a `database` that owns it until `Close`. This keeps
  session-scoped locks and any open transaction alive for exactly as long as
  the run needs them. `parse_connection` and the handshake use short-lived
  processes of their own.
- **Calls are serialized** by a mutex. There is no pipelining, so a response is
  matched to its request simply by reading the next line, and a mismatched `id`
  is treated as a protocol error that kills the connection.
- **Failure is sticky.** After a crash, a malformed response, an id mismatch or
  a cancelled call, the client records the error in `dead` and every later call
  returns it immediately. A plugin-reported `error` is different: it fails that
  one call and leaves the connection usable.
- **Cancellation kills the process.** While a call is in flight a goroutine
  watches the context and calls `Process.Kill`. A half-finished request cannot
  be resumed, and killing also releases any database session the plugin held. If
  the context is already cancelled before sending, the call returns early and
  the plugin is untouched.
- **Shutdown** closes stdin and waits up to 3 seconds, then kills. It never
  relies on SIGTERM, which Windows lacks. A non-zero exit during shutdown is
  ignored.
- **stderr** is wired to godwit's stderr, so plugin logs reach the user.
- **Executable lookup** (`resolve`) uses `exec.LookPath`, so Windows `PATHEXT`
  and the Unix executable bit are handled by the standard library.

`Close` sends `close` (best effort, 3 s) before stopping the process.
`Lock` returns an unlock function that uses a fresh 30 s context, because the
caller's context may already be cancelled when it releases the lock.

## What the plugin does and does not decide

godwit keeps the migration *policy*; the plugin supplies the *mechanism*.

- godwit picks the statements for the driver (`Migration.UpSQL(driver)` and
  `DownSQL`) and sends them in order. A plugin never reads migration files.
- godwit refuses `revert` when there are no Down statements for the driver,
  before contacting the plugin.
- The plugin decides how to run them: transaction or not, how to take the lock,
  and the column types of `godwit_migration`. The dirty-flag protocol is
  specified in [plugins.md](plugins.md) because godwit relies on it to detect
  half-applied migrations.

## The public package and the protocol types

The protocol is defined twice: by the `plugin` adapter (godwit's side, in
`protocol.go`) and by `pkg/godwit/serve.go` (the plugin's side). This is
deliberate. `pkg/` is public API that plugin authors import, so it must not
depend on `internal/`, and `internal/` stays free to change. The duplication is
small (request and response shapes, target, record, migration).

`pkg/godwit/godwit.go` mirrors `app.Database`, but with its own
types (`Target`, `Record`, `Migration`) so it has no dependency on godwit's
domain package. Its `Migration` carries the already-selected statements, which
is what the wire format carries.

`internal/adapters/plugin/pkg_compat_test.go` is what keeps the two sides in
step: it builds a plugin with `godwit.ServeIO` and drives it through the
real adapter, checking that targets, passwords, migrations, records, aliases and
`NoHost` survive the round trip. Any change to one side's JSON field names or
semantics fails that test until the other side matches.

## Adding a method or bumping the protocol

1. Add the request and result types to `protocol.go`.
2. Add the call in `database.go` (or `factory.go` for non-database calls).
3. Handle it in `pkg/godwit/serve.go`, and extend `Connection` or `Driver`
   if plugin authors must implement it. Adding a method to those interfaces
   breaks existing plugins at compile time, so prefer an optional interface that
   `Serve` detects with a type assertion.
4. Update the fake plugin in `plugin_test.go`, extend `pkg_compat_test.go`, and
   add a test.
5. Document it in [plugins.md](plugins.md).

Additive changes that old plugins can ignore (new optional fields) do not need a
version bump. Anything that changes the meaning of an existing method does:
increase `ProtocolVersion`. `probe` rejects a plugin whose handshake `protocol`
differs, so there is no negotiation. If you need to support two versions at
once, that has to be added to `probe` first.

## Tests

- `internal/adapters/plugin/plugin_test.go` runs the test binary as the plugin:
  `TestMain` checks the `GODWIT_FAKE_PLUGIN` environment variable and serves the
  protocol instead of running tests. This works on every OS with no external
  tools. It covers the lifecycle, plugin errors, crashes, cancellation,
  protocol mismatch, built-in name clashes and missing executables.
- `internal/domain/drivers_test.go` and `internal/app/drivers_test.go` cover the
  registry, `Combine`, plugin driver lookup, no-password drivers and plugin
  configuration.
- `internal/adapters/cli/plugin_test.go` covers the `plugin` subcommands,
  including that a plugin failing its probe is never saved.
- `internal/adapters/filestore/config_test.go` covers `plugins` persistence.
- `pkg/godwit/serve_test.go` tests `ServeIO` directly: handshake, state
  errors, cleanup at EOF, panic containment, 3 MiB lines and CRLF input.
- `examples/driver-jsonfile` is a real plugin built on `pkg/godwit`. CI builds it on Linux, macOS and
  Windows through `go build ./...`.

## Installing plugins

`godwit plugin install` is `plugin.Install` (build) followed by the same
register step `plugin add` uses (`pluginRegister` in `cli/plugin.go`).

`Install` (`adapters/plugin/install.go`):

1. `installTarget` normalizes the argument: it strips `http(s)://`, defaults to
   `@latest`, and rejects anything starting with `-`, containing whitespace, or
   naming a local path. This is what stops a "package" like `-toolexec=...` from
   becoming a flag to `go install`. It is the one place user text reaches a
   command line.
2. It requires `go` on `PATH`. godwit never downloads a toolchain.
3. It runs `go install <target>` with `GOBIN=<root>/.godwit/plugins`, passing the
   tool's output through, and writes a `.gitignore` (`*`) into that directory.
4. `binaryName` predicts the executable's name the way `go install` names it
   (the last path element, skipping a `/vN` major-version suffix), and `Install`
   checks the file exists, which catches packages that are not `main`.

The registered command is that bare name. `resolve` looks in `.godwit/plugins`
before `PATH`, so it finds the install without storing an absolute path, and
`config.json` stays portable between machines (each one runs its own install).

`pluginRegister` then probes the plugin. The name defaults to the handshake's
driver name, but it is only a label: `Factory` keys on the driver name, and the
label is used by `plugin remove` and `plugin list`. It refuses a plugin whose
driver is built in. Registering something identical to an existing entry is a
no-op for `add` and an "Updated" message for `install`, so re-running install
upgrades the binary in place.

Tests run offline: `install_test.go` puts a copy of the test binary named `go`
first on `PATH`. As `go install` it copies itself, a working fake plugin, to
`$GOBIN`, so the test can install it and then probe the result. The CLI tests
inject fake `Probe` and `Install` functions through `cli.PluginOps`.

## Known limits

- Plugins are loaded at startup, so a newly added plugin needs a restart.
- Every command starts every registered plugin once for the handshake.
- There is no sandboxing, signing or capability restriction: a plugin runs with
  the user's privileges and receives the target's password in `open`.
- One protocol version is supported at a time.
- `install` trusts whatever `go install` fetches. There is no checksum or
  signature check on the plugin itself, beyond what the Go module proxy and
  `go.sum` database provide for modules.
