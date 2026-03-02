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

The project has six Go modules:

| Module | Path | Purpose |
|--------|------|---------|
| `github.com/matgreaves/rig` | `go.mod` | Root module — zero external deps. Contains `client/`, `connect/`, `connect/httpx/` |
| `github.com/matgreaves/rig/internal` | `internal/go.mod` | Server internals — heavy deps (Docker SDK, gRPC, etc). Contains `spec/`, `server/`, `explain/`, `cmd/rigd/`, `testdata/`, integration tests |
| `github.com/matgreaves/rig/cmd/rig` | `cmd/rig/go.mod` | CLI tool — depends on `internal` for explain engine |
| `github.com/matgreaves/rig/connect/temporalx` | `connect/temporalx/go.mod` | Temporal client helper — isolates Temporal SDK dependency |
| `github.com/matgreaves/rig/connect/pgx` | `connect/pgx/go.mod` | Postgres client helper — isolates pgx/v5 dependency |
| `github.com/matgreaves/rig/connect/redisx` | `connect/redisx/go.mod` | Redis client helper — isolates go-redis/v9 dependency |
| `github.com/matgreaves/rig/examples` | `examples/go.mod` | Example apps and integration tests |

Sub-module integration tests (e.g. `connect/temporalx`, `connect/pgx`, `connect/redisx`, `examples/`) require a `rigd` binary — either run `make build` first or set `RIG_BINARY`.

## Project structure

- `client/` — Go client SDK (`rig.Up`, `rig.TryUp`, `rig.EnsureServer`, service builders)
- `cmd/rig/` — CLI tool for inspecting test logs and diagnosing failures
- `connect/` — zero-dependency shared types (`Endpoint`, `Wiring`, `ParseWiring`)
- `connect/httpx/` — HTTP client/server helpers built on rig endpoints
- `connect/temporalx/` — Temporal client helper (sub-module)
- `connect/pgx/` — Postgres client helper (sub-module)
- `connect/redisx/` — Redis client helper (sub-module)
- `examples/echo/` — minimal example: single Go HTTP service + test
- `examples/orderflow/` — full example: Postgres + Temporal + HTTP API
- `internal/explain/` — failure diagnosis engine (analyzes JSONL event logs)
- `internal/spec/` — shared spec types and validation
- `internal/server/` — rigd server: orchestrator, lifecycle, health checks, artifact cache, proxy
- `internal/cmd/rigd/` — rigd CLI entrypoint
- `internal/testdata/` — test service fixtures (echo, tcpecho, userapi, fail)
- `internal/integration/` — integration tests (require server + testdata)

## Debugging test failures

When a rig test fails, the test output automatically includes a condensed diagnosis showing response bodies, service crashes, and correlated stderr. This appears before file paths in the cleanup output, prefixed with `rig:`.

For deeper investigation, use the `rig` CLI (`cmd/rig/`). Each test that calls `rig.Up` produces a `.jsonl` log in `{RIG_DIR}/logs/`. Name matching is fuzzy — use the test name, not full paths.

```bash
# Start here — structured failure diagnosis
rig explain OrderFlow              # JSON (parseable)
rig explain OrderFlow -p           # pretty-printed

# List failures
rig ls --failed

# Deeper inspection when explain isn't enough
rig traffic OrderFlow              # all HTTP/gRPC/TCP traffic
rig traffic OrderFlow --detail 3   # expand request #3 with headers/bodies
rig logs OrderFlow                 # interleaved service logs
rig logs OrderFlow --service api   # single service

# Scripting
rig explain $(rig ls --failed -q -n1)            # most recent failure
rig ls --failed -q | xargs -I{} rig explain {}   # all failures
```

Key flags: `--failed`/`--passed` filter by outcome, `-q` outputs file paths for piping, `-n N` limits to N most recent results.

## Key conventions

- Service types are registered in `internal/cmd/rigd/main.go` and `internal/integration/integration_test.go:startTestServer`
- Endpoint attributes (e.g. `TEMPORAL_ADDRESS`, `PGHOST`) follow the convention of the upstream tool's env vars
- `github.com/matryer/is` for test assertions in server tests; stdlib `testing` in client tests
- `github.com/matgreaves/run` for concurrency primitives
- Wire types in `client/wire_types.go` are unexported copies of `internal/spec/` types — keep JSON tags in sync when modifying spec types
- The `internal/` path element prevents external consumers from importing server code — consumer-facing imports are `client/`, `connect/`, and `connect/httpx/`
