# rig

Language-agnostic test environment orchestrator. A standalone server (`rigd`) manages service lifecycles; thin client SDKs define environments via HTTP/JSON + SSE.

## Build & Test

```bash
make build          # Build rigd binary to ./bin/rigd
make test           # Build rigd, then run all tests with RIG_BINARY set
make clean          # Remove build artifacts, logs, and cache
```

`make test` builds `rigd` from `internal/cmd/rigd`, sets `RIG_BINARY`, then runs `go test ./...` in both the root module and `internal/`. Always use `make test` rather than bare `go test ./...`.

### Sub-modules

The project has three Go modules:

| Module | Path | Purpose |
|--------|------|---------|
| `github.com/matgreaves/rig` | `go.mod` | Root module — zero external deps. Contains `client/`, `connect/`, `connect/httpx/` |
| `github.com/matgreaves/rig/internal` | `internal/go.mod` | Server internals — heavy deps (Docker SDK, gRPC, etc). Contains `spec/`, `server/`, `cmd/rigd/`, `testdata/`, integration tests |
| `github.com/matgreaves/rig/connect/temporalx` | `connect/temporalx/go.mod` | Temporal client helper — isolates Temporal SDK dependency |

`connect/temporalx` integration test (`TestDial`) requires a `rigd` binary — either run `make build` first or set `RIG_BINARY`.

## Project structure

- `client/` — Go client SDK (`rig.Up`, `rig.TryUp`, `rig.EnsureServer`, service builders)
- `connect/` — zero-dependency shared types (`Endpoint`, `Wiring`, `ParseWiring`)
- `connect/httpx/` — HTTP client/server helpers built on rig endpoints
- `connect/temporalx/` — Temporal client helper (sub-module)
- `internal/spec/` — shared spec types and validation
- `internal/server/` — rigd server: orchestrator, lifecycle, health checks, artifact cache, proxy
- `internal/cmd/rigd/` — rigd CLI entrypoint
- `internal/testdata/` — test service fixtures (echo, tcpecho, userapi, fail)
- `internal/integration/` — integration tests (require server + testdata)

## Key conventions

- Service types are registered in `internal/cmd/rigd/main.go` and `internal/integration/integration_test.go:startTestServer`
- Endpoint attributes (e.g. `TEMPORAL_ADDRESS`, `PGHOST`) follow the convention of the upstream tool's env vars
- `github.com/matryer/is` for test assertions in server tests; stdlib `testing` in client tests
- `github.com/matgreaves/run` for concurrency primitives
- Wire types in `client/wire_types.go` are unexported copies of `internal/spec/` types — keep JSON tags in sync when modifying spec types
- The `internal/` path element prevents external consumers from importing server code — consumer-facing imports are `client/`, `connect/`, and `connect/httpx/`
