# rig

Language-agnostic test environment orchestrator. A standalone server (`rigd`) manages service lifecycles; thin client SDKs define environments via HTTP/JSON + SSE.

## Build & Test

```bash
make build          # Build rigd binary to ./bin/rigd
make test           # Build rigd, then run all tests with RIG_BINARY set
make clean          # Remove build artifacts, logs, and cache
```

`make test` builds `rigd` first and sets `RIG_BINARY` so that tests using `rig.Up()` can auto-discover the server. Always use `make test` rather than bare `go test ./...`.

### Sub-modules

`connect/temporalx` is a separate Go sub-module (`connect/temporalx/go.mod`) to isolate the Temporal SDK dependency. Test it separately:

```bash
cd connect/temporalx && go test ./...
```

Its integration test (`TestDial`) requires a `rigd` binary — either run `make build` first and ensure `~/.rig/bin/rigd` exists, or set `RIG_BINARY`.

## Project structure

- `spec/` — shared spec types and validation
- `server/` — rigd server: orchestrator, lifecycle, health checks, artifact cache, proxy
- `client/` — Go client SDK (`rig.Up`, service builders)
- `connect/` — zero-dependency shared types (`Endpoint`, `Wiring`, `ParseWiring`)
- `connect/httpx/` — HTTP client/server helpers built on rig endpoints
- `connect/temporalx/` — Temporal client helper (sub-module)
- `cmd/rigd/` — rigd CLI entrypoint

## Key conventions

- Service types are registered in `cmd/rigd/main.go` and `client/rig_test.go:startTestServer`
- Endpoint attributes (e.g. `TEMPORAL_ADDRESS`, `PGHOST`) follow the convention of the upstream tool's env vars
- `github.com/matryer/is` for test assertions in server tests; stdlib `testing` in client tests
- `github.com/matgreaves/run` for concurrency primitives
