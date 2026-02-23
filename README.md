# rig

Test environment orchestrator for Go. Define multi-service environments in code — Postgres, Temporal, Docker containers, Go binaries — and rig builds, starts, wires, and tears them down automatically.

```go
func TestAPI(t *testing.T) {
    env := rig.Up(t, rig.Services{
        "db":       rig.Postgres().InitSQLDir("./migrations"),
        "temporal": rig.Temporal(),
        "api":      rig.Go("./cmd/api").Egress("db").Egress("temporal"),
    })

    client := httpx.New(env.Endpoint("api"))
    resp, _ := client.Get("/health")
    // ...
}
```

No YAML. No manual port wiring. No cleanup code. Services start in dependency order, health checks pass, and you get back typed endpoints.

## How it works

A standalone server (`rigd`) manages service lifecycles. The Go SDK sends a declarative spec over HTTP, then streams events via SSE until the environment is ready. `rigd` handles building Go binaries, pulling Docker images, allocating ports, running health checks, and resolving wiring between services.

The SDK auto-starts `rigd` on first use — no separate install step needed. The binary is downloaded from GitHub Releases and cached in `~/.rig/bin/`.

## Install

```bash
go get github.com/matgreaves/rig
```

The root module has **zero external dependencies**. Your `go.sum` stays clean.

## Quickstart

```go
package myapp_test

import (
    "testing"

    rig "github.com/matgreaves/rig/client"
    "github.com/matgreaves/rig/connect/httpx"
)

func TestMyApp(t *testing.T) {
    env := rig.Up(t, rig.Services{
        "db":  rig.Postgres(),
        "api": rig.Go("./cmd/api").Egress("db"),
    })

    api := httpx.New(env.Endpoint("api"))
    resp, err := api.Get("/health")
    if err != nil {
        t.Fatal(err)
    }
    if resp.StatusCode != 200 {
        t.Fatalf("status %d", resp.StatusCode)
    }
}
```

Run with `go test`:

```bash
go test ./...
```

On first run, `rigd` is downloaded automatically. Postgres starts in Docker, the Go binary is built and launched with the right connection string, and everything tears down when the test finishes.

## Service types

### Go binary

Builds and runs a Go module. Default HTTP ingress.

```go
rig.Go("./cmd/api").
    Egress("db").
    Args("--verbose")
```

### In-process function

Runs a Go function in the test process. Same wiring interface as a binary — swap between `rig.Go` and `rig.Func` freely.

```go
rig.Func(myapp.Run).
    Egress("db").
    Egress("temporal")
```

### Docker container

Runs any Docker image. Set the container port with `.Port()`.

```go
rig.Container("redis:7").Port(6379)
rig.Container("nginx:alpine").Port(80).Env("NGINX_HOST", "localhost")
```

### Postgres

Managed Postgres container with automatic database creation and SQL init.

```go
rig.Postgres()
rig.Postgres().InitSQLDir("./migrations")
rig.Postgres().InitSQL("CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT)")
```

### Temporal

Managed Temporal dev server. Downloads the CLI binary on first use.

```go
rig.Temporal()
rig.Temporal().Version("1.5.1").Namespace("my-ns")
```

### Pre-built binary

Runs any executable.

```go
rig.Process("/usr/local/bin/myservice").
    Egress("db")
```

## Wiring

Services declare dependencies with `.Egress()`. Rig resolves the graph, starts services in order, and passes connection details as environment variables.

```go
rig.Services{
    "db":       rig.Postgres(),
    "cache":    rig.Container("redis:7").Port(6379),
    "api":      rig.Go("./cmd/api").Egress("db").Egress("cache"),
    "worker":   rig.Go("./cmd/worker").Egress("db").Egress("temporal").NoIngress(),
    "temporal": rig.Temporal(),
}
```

Each service receives its egress endpoints as environment variables. For Go services, use the `connect` package to read wiring:

```go
// In your service's main():
w, err := connect.ParseWiring(ctx)
dbEndpoint := w.Egress("db")  // connect.Endpoint with Host, Port, Attributes
```

## Endpoints and attributes

`env.Endpoint("service")` returns a `connect.Endpoint` with `Host`, `Port`, `Protocol`, and typed `Attributes`.

Built-in services publish well-known attributes:

```go
// Postgres
ep := env.Endpoint("db")
dsn := connect.PostgresDSN(ep) // "postgres://postgres:postgres@127.0.0.1:54321/mydb"

// Or use typed attributes directly
host := connect.PGHost.MustGet(ep)
port := connect.PGPort.MustGet(ep)

// Temporal
ep := env.Endpoint("temporal")
addr := connect.TemporalAddress.MustGet(ep)   // "127.0.0.1:7233"
ns := connect.TemporalNamespace.MustGet(ep)    // "default"
```

## Client helpers

Optional sub-modules provide typed clients that work with rig endpoints. Each is a separate Go module to isolate heavy dependencies.

### HTTP — `connect/httpx`

```go
import "github.com/matgreaves/rig/connect/httpx"

api := httpx.New(env.Endpoint("api"))
resp, err := api.Get("/users")
resp, err = api.Post("/users", "application/json", body)
```

### Postgres — `connect/pgx`

```go
import "github.com/matgreaves/rig/connect/pgx"

pool, err := pgx.Connect(ctx, env.Endpoint("db"))
db, err := pgx.OpenDB(env.Endpoint("db"))  // *sql.DB
```

### Temporal — `connect/temporalx`

```go
import "github.com/matgreaves/rig/connect/temporalx"

client, err := temporalx.Dial(env.Endpoint("temporal"))
```

## Hooks

Run setup code at specific lifecycle points:

```go
rig.Postgres().InitHook(func(ctx context.Context, w rig.Wiring) error {
    // Runs after health checks pass, before service is marked ready.
    // Receives full wiring.
    return runMigrations(w.Ingress())
})

rig.Go("./cmd/api").PrestartHook(func(ctx context.Context, w rig.Wiring) error {
    // Runs after egresses are resolved, before the process starts.
    // Receives full wiring (ingresses + egresses).
    return seedTestData(w.Egress("db"))
})

// SQL init hooks run server-side via docker exec — no SQL driver needed:
rig.Postgres().InitSQL("INSERT INTO users (name) VALUES ('alice')")

// Exec hooks run commands inside containers:
rig.Container("redis:7").Port(6379).Exec("redis-cli", "SET", "key", "value")
```

## Temp directories

Every service gets two scratch directories, available via `Wiring`:

- **`w.TempDir`** — per-service isolated workspace. Each service gets its own. Use it for config files, generated artifacts, or anything a single service needs.
- **`w.EnvDir`** — per-environment shared directory. All services in the same environment can read and write here. Use it for cross-service coordination (e.g. shared fixtures, config one service writes and another reads).

Both are created before services start and cleaned up on teardown.

A common pattern is writing config in a prestart hook and referencing it in args with `${RIG_TEMP_DIR}`:

```go
rig.Go("./cmd/api").
    Args("-c", "${RIG_TEMP_DIR}/config.json").
    PrestartHook(func(ctx context.Context, w rig.Wiring) error {
        cfg := buildConfig(w.Egress("db"))
        return os.WriteFile(
            filepath.Join(w.TempDir, "config.json"), cfg, 0o644,
        )
    })
```

## Options

```go
rig.Up(t, services,
    rig.WithTimeout(5*time.Minute),   // max startup wait (default: 2m)
    rig.WithServer("http://..."),      // explicit rigd URL (default: auto-start)
    rig.WithoutObserve(),              // disable traffic proxying
)
```

## Modules

| Module | Import path | Purpose |
|--------|-------------|---------|
| Root | `github.com/matgreaves/rig` | SDK + shared types. Zero deps. |
| `connect/httpx` | `github.com/matgreaves/rig/connect/httpx` | HTTP client/server helpers |
| `connect/pgx` | `github.com/matgreaves/rig/connect/pgx` | Postgres client (`pgxpool`, `*sql.DB`) |
| `connect/temporalx` | `github.com/matgreaves/rig/connect/temporalx` | Temporal client helper |

Server internals live in `internal/` and cannot be imported.

## Agentic coding

If you use an agentic coding tool (Claude Code, Cursor, Copilot), include [`agents-guide.md`](agents-guide.md) in your project context.

## License

MIT
