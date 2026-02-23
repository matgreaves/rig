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
ep := env.Endpoint("api") // connect.Endpoint{Host, Port, Protocol, Attributes}
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
addr := dbEp.Addr()           // "host:port"
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
- `examples/orderflow/` — full example: Postgres + Temporal + HTTP API
