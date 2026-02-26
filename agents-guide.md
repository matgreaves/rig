# rig — Agents Guide

Test environment orchestrator for Go. A standalone server (`rigd`) manages service lifecycles; the Go SDK defines environments declaratively in test code.

## Quick reference

```go
import rig "github.com/matgreaves/rig/client"

env := rig.Up(t, rig.Services{
    "db":       rig.Postgres().InitSQLDir("./migrations"),
    "temporal": rig.Temporal(),
    "api":      rig.Go("./cmd/api").Egress("db").Egress("temporal"),
})
ep := env.Endpoint("api") // connect.Endpoint{HostPort, Protocol, Attributes}
```

`rig.Up` blocks until all services are healthy and registers `t.Cleanup` for teardown. `rigd` is auto-downloaded on first use.

## Module structure

| Import | Purpose | Deps |
|--------|---------|------|
| `github.com/matgreaves/rig/client` | SDK: `Up`, `TryUp`, service builders | Zero |
| `github.com/matgreaves/rig/connect` | Shared types: `Endpoint`, `Wiring`, `ParseWiring`, typed `Attr[T]` | Zero |
| `github.com/matgreaves/rig/connect/httpx` | HTTP client/server from endpoints | Zero |
| `github.com/matgreaves/rig/connect/pgx` | `pgxpool.Pool` / `*sql.DB` from endpoint | pgx/v5 |
| `github.com/matgreaves/rig/connect/temporalx` | Temporal client from endpoint | Temporal SDK |

Root module has zero external dependencies. `connect/pgx` and `connect/temporalx` are separate Go modules to isolate heavy deps.

## Service types

| Constructor | What it runs | Default ingress |
|-------------|-------------|-----------------|
| `rig.Go("./cmd/api")` | Builds & runs Go module | HTTP |
| `rig.Func(myapp.Run)` | Runs function in test process | HTTP |
| `rig.Container("redis:7").Port(6379)` | Docker container | HTTP |
| `rig.Process("/path/to/bin")` | Pre-built binary | HTTP |
| `rig.Postgres()` | Managed Postgres container | Postgres (TCP) |
| `rig.Temporal()` | Managed Temporal dev server | gRPC |

All builders use method chaining: `.Egress("name")`, `.NoIngress()`, `.Ingress("name", def)`, `.Args(...)`, `.InitHook(fn)`, `.PrestartHook(fn)`.

## Wiring

Services declare dependencies with `.Egress("service")`. Rig resolves the dependency graph, starts services in order, and passes connection details.

**In services** — use `connect.ParseWiring(ctx)` to read wiring:
```go
w, _ := connect.ParseWiring(ctx)
dbEp := w.Egress("db")       // connect.Endpoint
addr := dbEp.HostPort         // "host:port"
dsn := connect.PostgresDSN(dbEp)
```

**In tests** — use `env.Endpoint("service")`:
```go
ep := env.Endpoint("api")
client := httpx.New(ep)
```

## Typed attributes

Built-in services publish well-known attributes on their endpoints:

```go
// Postgres
connect.PGHost.MustGet(ep)      // string
connect.PGPort.MustGet(ep)      // string
connect.PGDatabase.MustGet(ep)  // string
connect.PostgresDSN(ep)         // full DSN string

// Temporal
connect.TemporalAddress.MustGet(ep)    // "host:port"
connect.TemporalNamespace.MustGet(ep)  // "default"
```

Define custom attributes with `connect.Attr[T]{Name: "MY_ATTR"}`.

## Hooks

```go
// After health checks, before marked ready. Receives full wiring.
.InitHook(func(ctx context.Context, w rig.Wiring) error { ... })

// After egresses resolved, before process starts. Receives full wiring.
.PrestartHook(func(ctx context.Context, w rig.Wiring) error { ... })

// SQL init (server-side, no driver needed):
rig.Postgres().InitSQL("CREATE TABLE ...")
rig.Postgres().InitSQLDir("./migrations")

// Exec inside container (server-side):
.Exec("redis-cli", "SET", "key", "value")
```

## Temp directories

Available on `Wiring`:

- `w.TempDir` — per-service isolated workspace (config, artifacts)
- `w.EnvDir` — per-environment shared directory (cross-service coordination)

Args support `${RIG_TEMP_DIR}` expansion:
```go
rig.Go("./cmd/api").Args("-c", "${RIG_TEMP_DIR}/config.json").
    PrestartHook(func(ctx context.Context, w rig.Wiring) error {
        return os.WriteFile(filepath.Join(w.TempDir, "config.json"), cfg, 0o644)
    })
```

## Options

```go
rig.Up(t, services,
    rig.WithTimeout(5*time.Minute),  // default: 2m
    rig.WithServer("http://..."),     // default: auto-start rigd
    rig.WithoutObserve(),             // disable traffic proxying
)
```

## Traffic observability

By default, rig proxies every service edge and captures all HTTP requests, gRPC calls, and TCP connections in the event log (method, path, status, latency, headers, bodies up to 64KB). No instrumentation needed — rig controls the wiring. Disable with `rig.WithoutObserve()`.

`env.T` wraps `testing.TB` — assertion failures (`Fatal`, `Error`, etc.) are captured as `test.note` events with file:line info, interleaved with service output in the event log.

## Configuration

| Variable | Purpose | Default |
|----------|---------|---------|
| `RIG_DIR` | Base directory for rigd state | `~/.rig` |
| `RIG_BINARY` | Path to rigd binary (skips auto-download) | Auto-download |
| `RIG_PRESERVE` | Keep temp directories after teardown | Unset |
| `RIG_PRESERVE_ON_FAILURE` | Keep temp directories only on test failure | Unset |

## Debugging test failures

Each test that calls `rig.Up` produces a `.jsonl` log in `{RIG_DIR}/logs/`. Install the CLI with `go install github.com/matgreaves/rig/cmd/rig@latest`.

**Find logs by test name** — don't use full paths. Tests run in parallel so "most recent" is meaningless; use the test name:

```bash
# List all recent logs, or just failures
rig ls
rig ls --failed

# Inspect a specific test — fuzzy name matching, no path needed
rig traffic OrderFlow
rig logs OrderFlow
```

**Compose for scripting** — `rig ls -q` outputs file paths (one per line) for piping:

```bash
# Most recent failure
rig traffic $(rig ls --failed -q -n1)

# All failures
rig ls --failed -q | xargs -I{} rig traffic {}

# Most recent OrderFlow failure
rig logs $(rig ls --failed -q -n1 Order)
```

**Traffic inspection**:

```bash
rig traffic OrderFlow --detail 3            # expand request #3 — headers, bodies
rig traffic OrderFlow --edge "order→db"     # filter by service edge
rig traffic OrderFlow --slow 100ms          # only slow requests
rig traffic OrderFlow --status 5xx          # only server errors
```

**Service logs**:

```bash
rig logs OrderFlow                          # interleaved logs from all services
rig logs OrderFlow --service api            # filter to one service
rig logs OrderFlow --grep "connection refused"
```

Test assertions made via `env.T` (Fatal, Error, etc.) appear inline in `rig logs` as bold red markers with file:line info, interleaved with the service output that was happening at the time.

## Build & test (for rig contributors)

```bash
make build   # Build rigd to ./bin/rigd
make test    # Build + run all tests
make clean   # Remove artifacts
```

Five Go modules: root `go.mod`, `internal/go.mod`, `connect/temporalx/go.mod`, `connect/pgx/go.mod`, `examples/go.mod`. Always use `make test` — it sets `RIG_BINARY` and builds `rigd` first.

## Key files

- `client/rig.go` — `Up`, `TryUp`, `EnsureServer`, core types
- `client/services.go` — `Go`, `Func`, `Process`, `Custom` builders
- `client/container.go` — `Container` builder
- `client/postgres.go` — `Postgres` builder
- `client/temporal.go` — `Temporal` builder
- `client/environment.go` — `Environment`, `Endpoint()` lookup
- `connect/wiring.go` — `Wiring`, `ParseWiring`
- `connect/attrs.go` — `Attr[T]`, well-known attributes (`PGHost`, `TemporalAddress`, etc.)
- `connect/httpx/client.go` — `httpx.New`, HTTP client helpers
- `connect/httpx/server.go` — `httpx.ListenAndServe` for services
- `internal/server/` — rigd server (not importable by consumers)
- `docs/protocol.md` — rigd wire protocol reference (for building non-Go SDKs)
- `examples/echo/` — minimal example: single Go HTTP service + test
- `examples/orderflow/` — full example: Postgres + Temporal + HTTP API
