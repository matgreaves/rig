# rig

Language-agnostic test environment orchestrator. A standalone server (`rigd`) manages service lifecycles; thin client SDKs define environments via HTTP/JSON + SSE.

## Build & Test

```bash
make build          # Build rigd binary to ./bin/rigd
make test           # Build rigd, then run all tests with RIG_BINARY set
make clean          # Remove build artifacts, logs, and cache
```

`make test` builds `rigd` from `internal/cmd/rigd`, sets `RIG_BINARY`, then runs `go test ./...` in root, `internal/`, and `examples/`. Always use `make test` rather than bare `go test ./...`.

If you need to run tests for a single module with `go test` directly (e.g. `cd examples && go test ./orderflow/ -run TestOrderFlow -v`), you **must** run `make build` first so the `rigd` binary is up-to-date, and set `RIG_BINARY=$(pwd)/bin/rigd`. Tests auto-start a `rigd` process that idles for 5 minutes — if you changed server code, kill any stale `rigd` processes (`pkill rigd`) before re-running tests so a fresh binary is used.

### Sub-modules

The project has five Go modules:

| Module | Path | Purpose |
|--------|------|---------|
| `github.com/matgreaves/rig` | `go.mod` | Root module — zero external deps. Contains `client/`, `connect/`, `connect/httpx/` |
| `github.com/matgreaves/rig/internal` | `internal/go.mod` | Server internals — heavy deps (Docker SDK, gRPC, etc). Contains `spec/`, `server/`, `cmd/rigd/`, `testdata/`, integration tests |
| `github.com/matgreaves/rig/connect/temporalx` | `connect/temporalx/go.mod` | Temporal client helper — isolates Temporal SDK dependency |
| `github.com/matgreaves/rig/connect/pgx` | `connect/pgx/go.mod` | Postgres client helper — isolates pgx/v5 dependency |
| `github.com/matgreaves/rig/examples` | `examples/go.mod` | Example apps and integration tests |

Sub-module integration tests (e.g. `connect/temporalx`, `connect/pgx`, `examples/`) require a `rigd` binary — either run `make build` first or set `RIG_BINARY`.

## Project structure

- `client/` — Go client SDK (`rig.Up`, `rig.TryUp`, `rig.EnsureServer`, service builders)
- `connect/` — zero-dependency shared types (`Endpoint`, `Wiring`, `ParseWiring`)
- `connect/httpx/` — HTTP client/server helpers built on rig endpoints
- `connect/temporalx/` — Temporal client helper (sub-module)
- `connect/pgx/` — Postgres client helper (sub-module)
- `examples/echo/` — minimal example: single Go HTTP service + test
- `examples/orderflow/` — full example: Postgres + Temporal + HTTP API
- `internal/spec/` — shared spec types and validation
- `internal/server/` — rigd server: orchestrator, lifecycle, health checks, artifact cache, proxy
- `internal/cmd/rigd/` — rigd CLI entrypoint
- `internal/testdata/` — test service fixtures (echo, tcpecho, userapi, fail)
- `internal/integration/` — integration tests (require server + testdata)

## Debugging test failures with `rig` CLI

The `rig` CLI (`cmd/rig/`) inspects event logs written by `rigd`. Each test that calls `rig.Up` produces a `.jsonl` log file in `{RIG_DIR}/logs/`. Since `make test` runs many tests in parallel, always use the test name to find the right log — "most recent" is meaningless with parallel runs.

```bash
# See what failed
rig ls --failed

# Inspect a specific test's traffic (name matching is fuzzy — no path needed)
rig traffic OrderFlow
rig logs OrderFlow

# Compose for scripting
rig traffic $(rig ls --failed -q -n1)               # most recent failure
rig ls --failed -q | xargs -I{} rig traffic {}      # all failures
rig ls --failed -q -n1 Order                         # most recent OrderFlow failure
```

Key flags: `--failed`/`--passed` filter by outcome, `-q` outputs file paths for piping, `-n N` limits to N most recent results.

## Key conventions

- Service types are registered in `internal/cmd/rigd/main.go` and `internal/integration/integration_test.go:startTestServer`
- Endpoint attributes (e.g. `TEMPORAL_ADDRESS`, `PGHOST`) follow the convention of the upstream tool's env vars
- `github.com/matryer/is` for test assertions in server tests; stdlib `testing` in client tests
- `github.com/matgreaves/run` for concurrency primitives
- Wire types in `client/wire_types.go` are unexported copies of `internal/spec/` types — keep JSON tags in sync when modifying spec types
- The `internal/` path element prevents external consumers from importing server code — consumer-facing imports are `client/`, `connect/`, and `connect/httpx/`
