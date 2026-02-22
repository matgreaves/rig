# rig — Test Environment Orchestrator

## Vision

A **language-agnostic test environment orchestrator**. A standalone server (`rigd`) manages service lifecycles while client SDKs in any language define environments as native types, serialize them to JSON, and interact via an HTTP API. The server is auto-managed — SDKs download, cache, and start the correct server version transparently.

**Key differentiators from testcontainers:**
- Pluggable service backends (containers, processes, scripts, Go modules, builtins, client-side functions)
- Explicit ingress/egress wiring between services with automatic port allocation
- Run unlimited copies of the same environment definition simultaneously with zero port conflicts
- Event-driven lifecycle with hooks (prestart, init) for configuration and setup
- Artifact phase with global dedup/caching (Docker pulls, Go builds, downloads) before services start
- Extensible service type system — builtins ship with the server, custom types run on the client
- Central event bus for coordination and observability

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Test Suite                        │
│  (Go, TypeScript, Python, etc.)                     │
│                                                     │
│  resolved := rig.Up(t, rig.Env("my-test", ...))  │
│  // cleanup registered via t.Cleanup automatically  │
│  api := resolved.Endpoint("my-service")             │
└────────────────────┬────────────────────────────────┘
                     │ HTTP/JSON API
                     │ + HTTP callbacks (server → client)
                     ▼
┌─────────────────────────────────────────────────────┐
│                   rigd (server)                    │
│                                                     │
│  ┌────────────────────────────────────────────┐     │
│  │           ARTIFACT PHASE                   │     │
│  │  docker pull, go build, download           │     │
│  │  deduplicated + cached globally            │     │
│  └──────────────────┬─────────────────────────┘     │
│                     │ all artifacts ready            │
│  ┌──────────────────▼─────────────────────────┐     │
│  │           SERVICE PHASE                    │     │
│  │                                            │     │
│  │  ┌──────────┐ ┌──────────┐ ┌───────────┐  │     │
│  │  │container │ │ process  │ │  builtin  │  │     │
│  │  │ type     │ │  type    │ │   type    │  │     │
│  │  └────┬─────┘ └────┬─────┘ └─────┬─────┘  │     │
│  │       └─────────────┼─────────────┘        │     │
│  │         run.Group / Sequence               │     │
│  │    (concurrent with event-driven ordering) │     │
│  │                                            │     │
│  │           EVENT BUS                        │     │
│  │    coordination + observability            │     │
│  └────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────┘
```

### Why HTTP?

- **Universal** — every language has a good HTTP client and server library. No codegen, no protobuf dependency
- **SSE for streaming** — the client opens an SSE connection to rigd and receives all lifecycle events, callback requests, and log output on a single stream. No bidirectional connection needed — callback results are POSTed back as regular HTTP requests
- **JSON wire format** — the spec is already JSON. The API is JSON. SSE event data is JSON. No schema compilation step
- **Low SDK weight** — an SDK is just an HTTP client + an SSE stream reader. No server to run. A new language SDK can be a few hundred lines

### Versioning Model

The SDK embeds a content hash of the `rigd` engine binary it targets. This hash is the **only** coupling between the SDK and the server — there is no separate version number or compatibility matrix.

**Key properties:**

- **Client-only changes don't change the hash.** If you update the Go SDK's builder API, add a new helper function, or fix a bug in the SSE stream reader, the embedded engine hash stays the same. No re-download, no server restart. Tests keep using the already-running `rigd`.

- **Server changes produce a new hash.** If the engine gets a new service type, a bug fix, or a protocol change, the SDK is updated to embed the new hash. On next run, the SDK downloads and caches the new binary, starts it, and the old one idles out.

- **Breaking protocol changes are safe.** The SDK and the engine it targets are always compatible by construction — they ship as a pair. If we change the callback protocol, the JSON spec format, or the HTTP API contract, we update both the engine and the SDK at the same time. The new SDK embeds the new engine hash. Old SDKs continue to use old engines. There's no cross-version compatibility to maintain.

- **Multiple engine versions coexist.** Each hash gets its own directory under `~/.rig/bin/<hash>/rigd`. If two projects pin different SDK versions (and therefore different engine hashes), they each run their own `rigd` instance. The file lock is per-hash, not global.

- **Cache-friendly.** If a project hasn't changed its SDK dependency, the engine hash hasn't changed, so the cached binary is reused indefinitely. The download only happens once per engine version per machine.

```
~/.rig/
├── bin/
│   ├── a1b2c3d4/rigd     # engine for SDK v0.3.x
│   └── e5f6g7h8/rigd     # engine for SDK v0.4.x (protocol change)
├── cache/                   # shared artifact cache (all engine versions)
│   ├── docker/
│   ├── go-build/
│   └── downloads/
├── rigd-a1b2c3d4.lock     # per-hash file lock
├── rigd-a1b2c3d4.addr     # listen address of running server (e.g., "127.0.0.1:19432")
└── rigd-e5f6g7h8.addr     # each engine version has its own address file
```

This model is borrowed from Dagger's engine management but simplified — no OCI image to pull, just a single binary per platform.

### Server Lifecycle (Auto-Managed)

The SDK embeds a hash of the compatible `rigd` version. On first use:

1. SDK checks if a compatible `rigd` is already running by reading the address file `~/.rig/rigd-<hash>.addr`
2. If the file exists, probes the address with an HTTP health check to confirm liveness
3. If not running, checks `~/.rig/bin/` for the cached binary matching the embedded hash
4. If not cached, downloads the correct `rigd` binary and stores it in `~/.rig/bin/<hash>/rigd`
5. Acquires a per-hash file lock (`~/.rig/rigd-<hash>.lock`) to prevent multiple processes from starting multiple servers
6. Starts `rigd` as a background process (detached, with `onexit` cleanup registered)
7. `rigd` writes its listen address to `~/.rig/rigd-<hash>.addr` on startup, removes it on shutdown
8. Releases the lock — other processes waiting on it will now find rigd running at step 1

The address file is the discovery mechanism. Each engine version writes to its own file, so multiple engine versions can coexist without conflict. The file is removed on graceful shutdown; stale files from crashed processes are detected by the health check probe failing at step 2.

`rigd` shuts itself down after an idle timeout (e.g., 5 minutes with no active environments). This means:
- First test run in a session pays the startup cost (~milliseconds if cached)
- Subsequent test runs in the same session connect to the already-running server instantly
- The server doesn't linger indefinitely after tests finish
- CI environments get automatic cleanup

```go
// In the SDK — transparent to the user:
func ensureServer() (serverAddr string, err error) {
    addrFile := filepath.Join("~/.rig", fmt.Sprintf("rigd-%s.addr", embeddedHash))
    lockFile := filepath.Join("~/.rig", fmt.Sprintf("rigd-%s.lock", embeddedHash))

    // Try to connect to existing server via address file
    if addr, err := probeAddrFile(addrFile); err == nil {
        return addr, nil
    }

    // Acquire per-hash file lock to prevent races
    unlock, err := flock(lockFile)
    if err != nil {
        return "", err
    }
    defer unlock()

    // Check again after acquiring lock (another process may have started it)
    if addr, err := probeAddrFile(addrFile); err == nil {
        return addr, nil
    }

    // Download if needed
    binary := filepath.Join("~/.rig/bin", embeddedHash, "rigd")
    if _, err := os.Stat(binary); errors.Is(err, os.ErrNotExist) {
        if err := download(binary); err != nil {
            return "", err
        }
    }

    // Start server — it writes addrFile on startup
    return startDetached(binary, addrFile)
}
```

### SSE Event Stream Model

Instead of the server calling back to the client, the client subscribes to an SSE event stream and reacts to events — including requests to execute client-side functions. The client only ever makes outbound HTTP requests to rigd; rigd never needs to reach the client.

```
┌──────────────────────────────────────────────────────────────┐
│  Test Process                                                │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐    │
│  │  rig.Up(t, ...)                                     │    │
│  │                                                      │    │
│  │  1. POST /environments {spec}  → {id}                │    │
│  │  2. GET /environments/{id}/events (SSE stream)       │    │
│  │  3. Read events:                                     │    │
│  │     ─ callback.request → execute func, POST result   │    │
│  │     ─ service.log → t.Logf(...)                      │    │
│  │     ─ environment.up → return ResolvedEnvironment    │    │
│  │     ─ environment.failed → t.Fatal(...)              │    │
│  └──────────────┬───────────────────────────────────────┘    │
│                 │                                            │
└─────────────────┼────────────────────────────────────────────┘
                  │ All connections are client → server
                  ▼
┌──────────────────────────────────────────────────────────────┐
│  rigd                                                       │
│                                                              │
│  1. Receive spec, allocate environment, return ID            │
│  2. Stream all lifecycle events via SSE                      │
│  3. When lifecycle reaches a client_func:                    │
│     → publish callback.request event on SSE stream           │
│     → WaitFor callback.response event (blocks lifecycle)     │
│  4. Client executes, POSTs result to                         │
│     POST /environments/{id}/callbacks/{request_id}           │
│  5. Server publishes callback.response event                 │
│     → lifecycle step resumes                                 │
│  6. All ready → publish environment.up event                 │
└──────────────────────────────────────────────────────────────┘
```

This model means the SDK never runs a server — it only needs an HTTP client and an SSE stream reader. Every language has good libraries for both. No port allocation, no host discovery, no firewall issues. The same SSE stream that delivers callback requests also delivers service logs, lifecycle transitions, and failure events — one mechanism for coordination, observability, and debugging.

### Server vs SDK Responsibilities

```
SERVER (rigd) — thick, does the real work:
  ├── Standard service types (container, process, script, go, temporal, postgres, redis, ...)
  ├── Artifact resolution (docker pull, go build, download)
  ├── Port allocation
  ├── Wiring + env var mapping
  ├── Lifecycle orchestration (event bus, run.Group)
  ├── Health checks
  ├── Builtin hooks (initdb, create-namespace, ...)
  ├── Template expansion in args/config
  ├── SSE event streaming (lifecycle, callbacks, logs)
  └── Idle timeout + auto-shutdown

SDK (per language) — thin, just an HTTP client + SSE reader:
  ├── Spec builder (ergonomic native-language API → JSON)
  ├── HTTP client (create/destroy environments, post callback results)
  ├── SSE stream reader (receive lifecycle events, callback requests, logs)
  ├── Event loop (dispatch callback requests to registered handlers)
  ├── Auto-manage rigd lifecycle (download, cache, start, connect)
  └── Test framework integration (cleanup, assertions)

The SDK NEVER:
  ├── Manages containers
  ├── Allocates ports
  ├── Runs health checks
  ├── Runs an HTTP server
  └── Orchestrates service ordering
```

---

## Core Concepts

### Environment

A named collection of services with defined relationships. The same environment spec can be instantiated many times simultaneously — each instance gets its own ports, containers, temp dirs, and wiring.

### Service

Something that runs as part of the environment. Defined by:
- **Type** — how to start it (container, process, go, temporal, postgres, custom, ...)
- **Ingresses** (0–N) — what the service exposes
- **Egresses** (0–N) — what the service depends on (references to other services' ingresses)
- **Hooks** — prestart and init lifecycle hooks
- **Config** — type-specific configuration

### Ingress

An endpoint that a service exposes. Declared in the spec without concrete addresses — the server allocates ports at instantiation time. The service runner can enrich the ingress with generated attributes during the publish phase (e.g., Temporal generating a namespace name).

### Egress

A reference from one service to another service's ingress. Defines a dependency edge in the service graph.

### Endpoint

A fully resolved, concrete address produced at runtime:

```go
type Protocol string

const (
    TCP  Protocol = "tcp"
    HTTP Protocol = "http"
    GRPC Protocol = "grpc"
)

type Endpoint struct {
    Host       string         `json:"host"`
    Port       int            `json:"port"`
    Protocol   Protocol       `json:"protocol"`
    Attributes map[string]any `json:"attributes,omitempty"`
}
```

`Attributes` is the generic escape hatch — database name, credentials, namespace names, etc. Builtin service types name their attributes after standard env vars for their ecosystem (e.g., `PGHOST`, `PGPORT`, `TEMPORAL_ADDRESS`).

---

## Spec vs Runtime: Two Distinct Types

The spec declares topology (no addresses). The runtime resolves concrete values.

### Spec Types (what the user writes)

```go
type ReadySpec struct {
    Type     string         `json:"type"`               // "tcp", "http", "grpc" — overrides protocol default
    Path     string         `json:"path,omitempty"`     // for HTTP: GET path (default "/")
    Interval time.Duration  `json:"interval,omitempty"` // poll interval (default 100ms, exponential backoff)
    Timeout  time.Duration  `json:"timeout,omitempty"`  // max wait (default from global timeout config)
}

type IngressSpec struct {
    ContainerPort int            `json:"container_port,omitempty"` // for containers only
    Protocol      Protocol       `json:"protocol"`
    Ready         *ReadySpec     `json:"ready,omitempty"`
    Attributes    map[string]any `json:"attributes,omitempty"`
}

type EgressSpec struct {
    Service string `json:"service"`
    Ingress string `json:"ingress,omitempty"` // defaults to sole ingress
}
```

### Runtime Types (what the server produces)

```go
type ResolvedEnvironment struct {
    ID       string                     `json:"id"`
    Name     string                     `json:"name"`
    Services map[string]ResolvedService `json:"services"`
}

type ServiceStatus string

const (
    StatusPending  ServiceStatus = "pending"
    StatusStarting ServiceStatus = "starting"
    StatusHealthy  ServiceStatus = "healthy"
    StatusReady    ServiceStatus = "ready"
    StatusFailed   ServiceStatus = "failed"
    StatusStopping ServiceStatus = "stopping"
    StatusStopped  ServiceStatus = "stopped"
)

type ResolvedService struct {
    Ingresses map[string]Endpoint `json:"ingresses"`
    Egresses  map[string]Endpoint `json:"egresses"`
    Status    ServiceStatus       `json:"status"`
}
```

---

## Port Allocation

The server allocates **purely random ports** using the same approach as [`run/exp/ports`](https://github.com/matgreaves/run/tree/main/exp/ports) — bind to `:0`, let the OS assign a free port, then close the listener and return the port. This eliminates the need for configured port ranges and makes collisions between rigd instances, other services, and parallel CI jobs effectively impossible.

**The spec never contains concrete ports.** All host-facing ports are allocated at instantiation time.

- **Containers**: internal port is fixed (e.g., postgres listens on 5432 inside), host port is randomly allocated by rigd and mapped via Docker
- **Processes**: port is randomly allocated by rigd and injected via env vars / template expansion
- **Multiple instances**: each environment instance gets its own isolated port space — guaranteed by the OS

```go
type PortAllocator struct {
    mu        sync.Mutex
    allocated map[int]string // port → environment instance ID (tracking only)
}

func (a *PortAllocator) Allocate(instanceID string, n int) ([]int, error)
func (a *PortAllocator) Release(instanceID string)
```

`Allocate` calls `net.Listen("tcp", ":0")` for each requested port, records the assigned port, closes the listener, and returns the ports. The in-process tracking map prevents the same rigd instance from handing out a port that's already in use by another active environment. The OS prevents collisions with everything else.

There is a small TOCTOU window between closing the listener and the service actually binding the port. In practice this is negligible — the port is used within milliseconds. If a service fails to bind, the error is clear and the environment fails fast.

No port ranges, no configuration, no cross-process file locks for port coordination. The OS is the allocator.

---

## Service Lifecycle (Event-Driven)

Every service runs this lifecycle sequence concurrently with every other service in the environment. Dependency ordering emerges from blocking on the event bus — no explicit topological sort needed.

```
┌──────────┐
│ PUBLISH  │  Allocate ports + let the runner generate attributes.
└────┬─────┘  e.g., Temporal runner generates namespace name here.
     │
┌────▼──────────────┐
│ WAIT FOR EGRESSES │  Block until every egress target is READY (step 7).
└────┬──────────────┘  Receives resolved Endpoint for each egress.
     │                 Cyclic graphs are caught at validation time (see Spec Validation).
┌────▼─────┐
│ PRESTART │  Hook (optional). Has full wiring: own ingresses + resolved egresses.
└────┬─────┘  Use case: write a config file, transform env vars.
     │
┌────▼─────┐
│  START   │  Launch the container/process/binary/builtin.
└────┬─────┘
     │
┌────▼─────┐
│  READY   │  Protocol-aware health check on each ingress.
└────┬─────┘  TCP dial / HTTP HEAD / gRPC health.
     │
┌────▼─────┐
│   INIT   │  Hook (optional). Receives own ingress details only.
└────┬─────┘  Use case: seed DB, create Temporal search attributes.
     │
┌────▼──────────┐
│  MARK READY   │  Emit event. Unblocks dependents waiting at step 2.
└────┬──────────┘
     │
┌────▼─────┐
│   IDLE   │  Service is running. Wait for teardown signal.
└──────────┘
```

### Mapping to `run` library

The [`run`](https://github.com/matgreaves/run) library provides the concurrency primitives:

- `run.Group` — all services run concurrently; if one fails, all are cancelled
- `run.Sequence` — each lifecycle step runs in order within a service
- `run.Func` — wraps a function as a Runner
- `run.Idle` — blocks until context cancellation
- `run.Start` / `run.Ready` — start + readiness check pattern

```go
func orchestrate(env spec.Environment, bus *EventBus, ports *PortAllocator) run.Runner {
    group := run.Group{}
    for name, svc := range env.Services {
        group[name] = serviceLifecycle(name, svc, bus, ports)
    }
    return group
}

func serviceLifecycle(name string, svc spec.Service, bus *EventBus, ports *PortAllocator) run.Runner {
    return run.Sequence{
        publishStep(name, svc, bus, ports),
        waitForEgressesStep(name, svc, bus),
        prestartStep(name, svc, bus),
        startStep(name, svc, bus),
        readyCheckStep(name, svc, bus),
        initStep(name, svc, bus),
        markReadyStep(name, bus),
        run.Idle,
    }
}
```

### Progress Watchdog

Even though cycle detection at validation time should prevent deadlocks, a runtime progress watchdog provides a safety net. The orchestrator tracks the last time any service changed lifecycle phase. If no service has made progress for a configurable duration (default 30s), the watchdog logs a diagnostic snapshot:

- Which services are blocked and in which phase
- For services stuck in `WAIT_FOR_EGRESSES`: which upstream services they're waiting on and those services' current status
- The full event log tail for context

This turns a mysterious 2-minute timeout into an actionable debug message within 30 seconds. The watchdog does not abort the environment — it only logs. The environment startup timeout is still the hard boundary.

### Teardown

All services receive context cancellation simultaneously (via `run.Group`). No ordering — optimise for teardown speed. Graceful shutdown waits up to a configurable duration (default 10s) before force-killing. After all services exit, per-service temp directories are cleaned up (unless `RIG_PRESERVE_ON_FAILURE` is set — see Debugging Support).

Any stage failure (publish, prestart, start, ready check, init, or hook) immediately fails the service, emits `EventServiceFailed` with the error, and triggers teardown of the entire environment via `run.Group` cancellation. For init hook failures, the service is stopped before the failure event is emitted. The error propagates back to the client as a structured error response (see Error Responses).

---

## Hooks

Hooks run at specific lifecycle stages. They support a subset of runner types — things that make sense for short-lived tasks:

| Hook | Receives | Purpose |
|------|----------|---------|
| `prestart` | Full wiring: own ingresses + resolved egresses, temp dir | Write config files, transform env, prepare filesystem |
| `init` | Own ingress details only (how to talk to *this* service) | Seed data, create resources on the now-healthy service |

### Hook Types

```
client_func   — inline Go function (or registered in other SDKs)
script        — shell command
builtin       — service-type-specific action (initdb, create-namespace, etc.)
```

### Builtin Hooks per Service Type

```
POSTGRES:   initdb (run SQL migrations), create-database
TEMPORAL:   create-namespace, create-search-attributes
CONTAINER:  write-config-file (template a config file into temp dir)
ANY:        script (run a shell command), client_func (callback to client)
```

### Init Hook Attribute Convention

Init hooks receive the service's ingress attributes as env vars. Builtin service types name attributes after the standard env vars for their ecosystem's CLI tools:

- **Postgres**: `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD` — `psql` and migration tools just work
- **Temporal**: `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE` — `temporal` CLI just works
- **Redis**: `REDIS_URL` — `redis-cli` just works

---

## Attribute Naming & Environment Variable Mapping

Three levels of attributes, with simple rules: egresses are always prefixed by name, init hooks receive ingress attributes with the default ingress unprefixed.

### Attribute Levels

1. **Service-level** — about the service instance itself (e.g., `RIG_TEMP_DIR`, `RIG_ENV_DIR`, `RIG_SERVICE`)
2. **Ingress-level** — about each ingress endpoint (e.g., `PGHOST`, `PGPORT`, `PGDATABASE`)
3. **Egress-level** — resolved from the target's ingress attributes

### Env Var Mapping Rules

**For init hooks (own service's ingress attributes only — no egresses):**
- Default ingress: `ATTRNAME` (e.g., `PGHOST`, `PGPORT`) — unprefixed
- Additional named ingresses: `INGRESSNAME_ATTRNAME` (e.g., `ADMIN_PGHOST`)

Init hooks receive only the service's own ingress attributes — they have no access to egress attributes. This is deliberate: init hooks exist to configure *this* service (seed the database, create a namespace), not to reach its dependencies. The default ingress is unprefixed so that ecosystem tools (`psql`, `temporal`, `redis-cli`) just work. Additional named ingresses are prefixed by their ingress name to avoid collisions.

**For egresses (injected into a service's env and templates):**
- Always prefixed by egress name: `EGRESSNAME_ATTRNAME` (e.g., `DATABASE_PGHOST`, `TEMPORAL_TEMPORAL_ADDRESS`)

Egresses are always prefixed, even when there's only one. This means adding a second egress to a service never changes the env var names of the first — no silent breakage, no surprises. The egress name is always part of the contract.

**Service-level attributes**: always unprefixed (`RIG_TEMP_DIR`, `RIG_ENV_DIR`, `RIG_SERVICE`, `RIG_INSTANCE`)

### Example

A postgres service with a default ingress publishes attributes `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`.

- Its **init hook** receives them unprefixed → `psql` and migration tools just work
- A service with an egress `database` to postgres receives them as `DATABASE_PGHOST`, `DATABASE_PGPORT`, etc.
- A service with egresses `orders_db` and `users_db` to two postgres instances receives `ORDERS_DB_PGHOST`, `USERS_DB_PGHOST` — same rule, no special case

---

## Template Expansion

Service arguments, environment variable overrides, and hook arguments support `$VAR` and `${VAR}` references that are expanded against the fully resolved attribute map before execution.

Uses `os.Expand` from the Go stdlib — no custom parser needed.

### Template Context

The full merged `map[string]string` of:
- Service-level attributes (`RIG_TEMP_DIR`, `RIG_ENV_DIR`, `RIG_SERVICE`, etc.)
- Resolved ingress attributes (unprefixed for default/single, prefixed for named)
- Resolved egress attributes (always prefixed by egress name)

### Example

```json
{
  "order-service": {
    "type": "go",
    "config": {"module": "./cmd/order-service"},
    "args": ["-c", "${RIG_TEMP_DIR}/config.json", "--port", "${PORT}"],
    "hooks": {
      "prestart": {
        "type": "client_func",
        "client_func": {"name": "write-order-config"}
      }
    }
  }
}
```

The prestart hook writes a config file to `RIG_TEMP_DIR`. The args reference it via `${RIG_TEMP_DIR}/config.json`. The `${PORT}` expands to the allocated port for the default ingress.

---

## Artifact Phase

Before any services start, all artifacts are resolved. This prevents timeouts where service A is waiting for service B which is still pulling a Docker image. Every step emits events to the event log (`EventArtifactStarted`, `EventArtifactCompleted`, `EventArtifactCached`, `EventArtifactFailed`) so the full artifact resolution waterfall is observable and replayable.

```
┌──────────────────────────────────────────────────────────┐
│                    ARTIFACT PHASE                        │
│                                                          │
│  1. Collect artifacts from all services                  │
│  2. Deduplicate by key (same Go module → one build)      │
│  3. Check global cache (~/.rig/cache/)                  │
│     → emit EventArtifactCached for hits                  │
│  4. Resolve uncached artifacts in parallel               │
│     → emit EventArtifactStarted on begin                 │
│     → emit EventArtifactCompleted on success             │
│     → emit EventArtifactFailed on failure                │
│     - docker pull postgres:16                            │
│     - go build ./cmd/order-service                       │
│     - curl -o temporal https://...                       │
│  5. Store results in cache                               │
│                                                          │
│  No timeouts from egress waiting. No service graph yet.  │
└────────────────────────┬─────────────────────────────────┘
                         │ all artifacts resolved
┌────────────────────────▼─────────────────────────────────┐
│                    SERVICE PHASE                          │
│                                                          │
│  For each service concurrently:                          │
│    publish → wait egresses → prestart → start →          │
│    ready → init → mark ready → idle                      │
│                                                          │
│  Artifacts already local. docker run is instant.         │
│  Go binary already compiled. No surprises.               │
└──────────────────────────────────────────────────────────┘
```

### Artifact Types

- **DockerImage** — `docker pull image:tag`
- **GoBuild** — `go build -o <cache_path> <module>` (see GoBuild Cache Strategy below)
- **Download** — fetch a URL to local path (cached by URL + checksum)
- **Command** — run an arbitrary command that produces an artifact

### Retry & Fail-Fast

Artifact resolution is optimised for **fail-fast**. Network-dependent artifacts (`DockerImage`, `Download`) retry up to 2 times with short exponential backoff (1s, 2s) on transient failures (connection reset, HTTP 5xx, timeout). Local operations (`GoBuild`, `Command`) do not retry — a build failure is a real failure.

If any artifact fails after retries, the entire artifact phase aborts immediately and the environment fails with a structured error identifying the failed artifact, the number of attempts, and the last error. No partial environments. No services start if an artifact is missing.

### Global Cache

Content-addressable store at `~/.rig/cache/`:

```
~/.rig/cache/
├── docker/           # Docker image pre-pull tracking
├── go-build/         # Compiled Go binaries keyed by source hash
├── downloads/        # Downloaded files keyed by URL + checksum
```

Key properties:
- **Deduplication** — same artifact referenced by multiple services or environments → resolved once
- **Cross-process coordination** — file locks prevent duplicate work when multiple rigd instances run simultaneously
- **Freshness** — cache key is a content hash of inputs (see per-type strategies below). Rebuild only when inputs change
- **Persistence** — survives across test runs. First run compiles; subsequent runs are instant

### GoBuild Cache Strategy

Go builds must be ruthlessly optimised. Even incremental `go build` invocations add measurable latency scanning the module graph. The rig cache skips `go build` entirely when inputs haven't changed:

1. **Cache key computation** — hash of the source tree rooted at the module directory. The cache key is computed from `go.mod` + `go.sum` + a recursive hash of the source tree. If the module is inside a git repository, we use `git ls-files` + `git diff` to compute the tree hash — this is extremely fast (git already tracks file state) and naturally excludes build artifacts, vendor noise, and other non-source files. Outside of git, we fall back to `filepath.WalkDir` with a streaming hasher over all files. Build tags and `GOOS`/`GOARCH` are included in the key.
2. **Cache hit** — if a binary exists at `~/.rig/cache/go-build/<hash>/binary`, use it directly. Zero subprocess invocations. Startup is instant.
3. **Cache miss** — invoke `go build -o <cache_path> <module>`, store the binary, record the hash.
4. **Invalidation** — any source file change, `go.mod` change, dependency update, or build tag change produces a different hash → cache miss → rebuild. No stale binaries.

The key insight: computing the cache key (hashing the source tree) is cheap (~milliseconds for a typical service module, near-instant with git). Actually running `go build` is expensive (~seconds even when incremental). By front-loading the hash check, we avoid invoking `go build` on every test run when nothing has changed.

The approach is deliberately not Go-aware beyond knowing about `go.mod`/`go.sum`. We hash the source tree, not individual `.go` files. This means the cache also invalidates correctly for embedded files, C sources, assembly, and anything else `go build` might consume.

### Artifact Interface

```go
type Artifact struct {
    Key  string           // globally unique key for dedup
    Type ArtifactResolver
}

type ArtifactOutput struct {
    Path string
    Meta map[string]string
}

type ArtifactResolver interface {
    CacheInputs() ([]byte, error)
    Resolve(ctx context.Context, outputDir string) (ArtifactOutput, error)
    Retryable() bool // true for network operations (DockerImage, Download), false for local (GoBuild, Command)
}
```

---

## Service Type System

A service type is an interface — not a fixed enum. The server ships with standard types. Users can register custom types that participate fully in the lifecycle.

### ServiceType Interface

```go
type ServiceType interface {
    Artifacts() []Artifact
    Publish(ctx context.Context, params PublishParams) (map[string]Endpoint, error)
    Start(ctx context.Context, params StartParams) error
    Ready(ctx context.Context, endpoint Endpoint) error // optional override
}
```

### Standard Types (server-side, any SDK gets these for free)

| Type | Description | Artifact |
|------|-------------|----------|
| `container` | Generic Docker container | DockerImage pre-pull |
| `process` | Run a binary with args | None |
| `script` | Run a shell script | None |
| `go` | Compile Go module + run | GoBuild |
| `postgres` | Postgres container with PG env vars | DockerImage |
| `temporal` | Temporal dev server from binary | Download |
| `redis` | Redis container | DockerImage |

### Custom Types (client-side, escape hatch)

Users implement `ServiceType` in their language's SDK. The server delegates lifecycle calls via the SSE event stream:

```
Server → Client (SSE): callback.request {type: "publish", service: "my-svc", request_id: "abc", ...}
Client → Server (POST): /environments/{id}/callbacks/abc  {ingresses: {...}}

Server → Client (SSE): callback.request {type: "start", service: "my-svc", request_id: "def", ...}
Client → Server (POST): /environments/{id}/callbacks/def  {ok: true}

Server → Client (SSE): callback.request {type: "ready", service: "my-svc", request_id: "ghi", ...}
Client → Server (POST): /environments/{id}/callbacks/ghi  {ready: true}
```

The pattern is the same as for `client_func` hooks: server publishes a `callback.request` event, blocks via `WaitFor` on the matching `callback.response`, and the client POSTs the result back. The SDK's event loop handles both hook callbacks and custom type lifecycle callbacks uniformly.

### The Spectrum

```
FULLY SERVER-SIDE (any SDK gets these for free):
  "type": "postgres"      — pull image, start container, PG env vars
  "type": "temporal"       — download binary, start, generate namespace
  "type": "go"             — compile module, run as process
  "type": "container"      — generic container with user-specified image

HYBRID (server runs it, client provides hooks):
  "type": "postgres" + init hook as client_func
    — server handles postgres lifecycle
    — calls back to client for custom init logic

FULLY CLIENT-SIDE (escape hatch):
  Custom ServiceType implementation
    — server orchestrates lifecycle ordering + event bus
    — calls back to client for publish, start, ready, everything
```

---

## Client-Side Functions (Killer Feature)

The ability to run functions on the client side as part of environments — closing over test-local state for inspection and assertions.

### How It Works

1. Client SDK defines a function inline in the spec (Go closure, JS arrow function, etc.)
2. SDK registers the function locally keyed by a generated name (e.g., `write-order-config`)
3. Spec is serialized to JSON with a `client_func` reference
4. When rigd reaches that point in the lifecycle, it publishes a `callback.request` event on the SSE stream
5. The event contains the resolved wiring as context and a unique request ID
6. The SDK's event loop dispatches to the registered function, which has access to test-local state
7. The function returns, the SDK POSTs the result to `/environments/{id}/callbacks/{request_id}`
8. rigd receives the result, publishes a `callback.response` event, and the lifecycle continues

### Example

```go
func TestOrders(t *testing.T) {
    var db *sql.DB

    resolved := rig.Up(t, rig.Env("order-test",
        rig.Postgres(rig.InitDB("./testdata/migrations")),
        rig.Service("order-service",
            rig.Go("./cmd/order-service"),
            rig.Egress("database", "postgres"),
            rig.Init(func(ctx context.Context) error {
                // Closes over `db` for later assertions.
                ep := rig.EndpointFromContext(ctx)
                db = connectTo(ep)
                return nil
            }),
        ),
    ))

    api := resolved.Endpoint("order-service")
    resp, _ := http.Post(fmt.Sprintf("http://%s:%d/orders", api.Host, api.Port), ...)

    // db was captured by the init closure — use it for assertions.
    var count int
    db.QueryRow("SELECT count(*) FROM orders").Scan(&count)
    assert.Equal(t, 1, count)
}
```

---

## Smart Defaults

### No Ingresses

A service with no explicit ingresses is valid — it simply has no exposed endpoints and no health checks. This is appropriate for workers, scripts, cron jobs, and other services that only consume from dependencies.

```go
rig.Service("worker", rig.Process("./my-worker"),
    rig.Egress("queue", "rabbitmq"),
)
// worker has no ingresses — no ports allocated, no health checks.
// Other services cannot reference it as an egress target.
```

### Single Ingress Shorthand

Egresses to a single-ingress service don't need the ingress name:

```go
rig.Egress("database", "postgres")
// resolves to postgres's sole ingress automatically
// errors clearly if postgres has multiple ingresses
```

### Endpoint Lookup

```go
api := resolved.Endpoint("my-service")                   // default/single ingress
frontend := resolved.Endpoint("temporal", "frontend")    // named ingress
```

---

## Event Bus (Log-Based)

The event bus is a **persistent, ordered event log** — not a fire-and-forget pub/sub system. Every event published is appended to an in-memory log with a monotonically increasing sequence number. Subscribers can replay from any point in the log, which means late subscribers never miss events and the log itself becomes a complete, queryable record of everything that happened during an environment's lifetime.

This design serves three purposes:

1. **Coordination** — services call `WaitFor` to block until a matching event appears. Because `WaitFor` replays the log from the beginning, it works correctly even if the event was published before the subscriber started waiting. No race conditions, no dropped coordination events.
2. **Observability** — external consumers (logging, monitoring, client streams, future UI) can subscribe at any time and replay the full history.
3. **Post-mortem analysis** — the complete event log is available after the environment shuts down. A visualisation tool can consume the log to render timelines, dependency graphs, and failure chains. The log can also be serialized to disk for offline debugging.

### Event Types

```go
const (
    // Artifact phase
    EventArtifactStarted   = "artifact.started"
    EventArtifactCompleted = "artifact.completed"
    EventArtifactFailed    = "artifact.failed"
    EventArtifactCached    = "artifact.cached"

    // Service lifecycle
    EventIngressPublished  = "ingress.published"
    EventWiringResolved    = "wiring.resolved"
    EventServicePrestart   = "service.prestart"
    EventServiceStarting   = "service.starting"
    EventServiceHealthy    = "service.healthy"
    EventServiceInit       = "service.init"
    EventServiceReady      = "service.ready"
    EventServiceFailed     = "service.failed"
    EventServiceStopping   = "service.stopping"
    EventServiceStopped    = "service.stopped"
    EventServiceLog        = "service.log"

    // Client-side callbacks (coordination between server and SDK)
    EventCallbackRequest   = "callback.request"
    EventCallbackResponse  = "callback.response"

    // Environment lifecycle
    EventEnvironmentUp     = "environment.up"
    EventEnvironmentDown   = "environment.down"
)

type LogEntry struct {
    Stream string `json:"stream"` // "stdout" or "stderr"
    Data   string `json:"data"`
}

type CallbackRequest struct {
    RequestID string         `json:"request_id"` // unique ID for correlation
    Name      string         `json:"name"`       // handler name (e.g., "write-order-config")
    Type      string         `json:"type"`       // "hook", "publish", "start", "ready"
    Wiring    *WiringContext `json:"wiring"`     // context depends on hook type (see below)
}

// WiringContext contents vary by callback type:
//   prestart hook: ingresses + egresses + temp dir + env dir
//   init hook:     ingresses only + temp dir (no egresses — init targets this service, not its dependencies)
//   custom type callbacks (publish/start/ready): type-specific

type CallbackResponse struct {
    RequestID string         `json:"request_id"` // matches the request
    Error     string         `json:"error,omitempty"`
    Data      map[string]any `json:"data,omitempty"` // type-specific response (e.g., ingresses for publish)
}

type Event struct {
    Seq         uint64           // monotonically increasing sequence number
    Type        EventType
    Environment string
    Service     string
    Ingress     string
    Endpoint    *Endpoint
    Artifact    string           // artifact key (for artifact events)
    Log         *LogEntry
    Callback    *CallbackRequest // for callback.request events
    Result      *CallbackResponse // for callback.response events
    Error       string
    Timestamp   time.Time
}
```

**Callback coordination via the event log**: when the server reaches a `client_func` in the lifecycle, it publishes a `callback.request` event and calls `WaitFor` on a `callback.response` event with the matching `RequestID`. The SDK reads the request from its SSE stream, executes the handler, and POSTs the result to `/environments/{id}/callbacks/{request_id}`. The server receives the POST, publishes a `callback.response` event, and the blocked lifecycle step resumes. This uses the same `WaitFor` mechanism as egress dependency waiting — no new coordination primitive needed.

**SSE streaming**: the SSE endpoint (`GET /environments/{id}/events`) is backed by `Subscribe`. When a client connects, it replays all events from the beginning of the environment's lifetime and then streams new events as they arrive. This means a client that connects mid-lifecycle (e.g., a debugging tool attached to a running environment) sees the full history. The SSE stream is the primary interface between rigd and SDKs — it delivers lifecycle transitions, callback requests, service logs, and terminal events on a single connection.

### Event Log Interface

```go
type EventLog struct {
    mu     sync.Mutex
    events []Event
    seq    uint64
    notify chan struct{} // closed + replaced on each new event to wake waiters
}

func (l *EventLog) Publish(event Event)
func (l *EventLog) Subscribe(ctx context.Context, fromSeq uint64, filter func(Event) bool) <-chan Event
func (l *EventLog) WaitFor(ctx context.Context, match func(Event) bool) (Event, error)
func (l *EventLog) Events() []Event  // snapshot of the full log
func (l *EventLog) Since(seq uint64) []Event
```

**Log, not channels**: `Publish` appends to the log and wakes all waiters. `Subscribe` replays events from `fromSeq` through the current tail, then streams new events as they arrive. `WaitFor` scans the existing log first — if a matching event already exists, it returns immediately without blocking. Otherwise it waits for new events until a match is found or the context is cancelled.

**Context cancellation**: `WaitFor` returns `ctx.Err()` (either `context.DeadlineExceeded` or `context.Canceled`) when the context is cancelled before a matching event arrives. This is the mechanism that produces timeout diagnostics — the caller knows both *what* it was waiting for and *why* it stopped waiting.

**Ordering**: events are sequenced globally by the log. Within a single service's lifecycle, events are naturally ordered by the `run.Sequence` that produces them. Cross-service ordering reflects the actual order of `Publish` calls (protected by the mutex), which closely tracks wall-clock time.

**Backpressure**: `Subscribe` channels are buffered (default 256). If a subscriber falls behind, events accumulate in the channel. If the channel is full, new events for that subscriber are dropped with a warning — publishers never block. This only affects streaming subscribers (SSE, terminal loggers). Coordination via `WaitFor` reads directly from the log and is never affected by channel backpressure.

**Visualisation**: because the event log is a complete, ordered record with sequence numbers and timestamps, it can be consumed by external tools. A future `rig visualise` command or web UI can read the log (via the SSE endpoint or from a serialized file) and render:
- A timeline view showing when each service entered each lifecycle phase
- A dependency graph with edges annotated by wait duration
- Failure chains showing which service failed, what was waiting on it, and the cascade effect
- Artifact resolution waterfall (parallel pulls/builds with durations)

---

## Health Checks

Protocol-driven readiness checks, inferred from the ingress protocol:

| Protocol | Check |
|----------|-------|
| TCP | `net.Dial` until success |
| HTTP | HTTP `HEAD` until non-5xx response |
| gRPC | `grpc.health.v1.Health/Check` |

Uses exponential backoff polling (reusing patterns from `run/exp`).

The spec can override the check type explicitly via `"ready": {"type": "tcp"}`, but the default is inferred from the protocol.

Service types can override the `Ready` method for custom readiness logic.

---

## Environment Spec (JSON Wire Format)

This is the contract between every SDK and the server:

```json
{
  "name": "order-workflow",
  "services": {
    "postgres": {
      "type": "postgres",
      "config": {
        "database": "orders",
        "user": "test",
        "password": "test"
      },
      "hooks": {
        "init": {
          "type": "initdb",
          "config": {"migrations": "./testdata/migrations"}
        }
      }
    },
    "temporal": {
      "type": "temporal",
      "hooks": {
        "init": {
          "type": "create-search-attributes",
          "config": {"OrderID": "Keyword", "CustomerID": "Keyword"}
        }
      }
    },
    "order-service": {
      "type": "go",
      "config": {"module": "./cmd/order-service"},
      "args": ["-c", "${RIG_TEMP_DIR}/config.json"],
      "ingresses": {
        "api": {
          "protocol": "http",
          "ready": {"type": "http"}
        }
      },
      "egresses": {
        "database": {"service": "postgres"},
        "temporal": {"service": "temporal", "ingress": "frontend"}
      },
      "hooks": {
        "prestart": {
          "type": "client_func",
          "client_func": {"name": "write-order-config"}
        }
      }
    }
  }
}
```

---

## Go SDK API

```go
func TestFullStack(t *testing.T) {
    var db *sql.DB

    resolved := rig.Up(t, rig.Env("full-stack",
        rig.Postgres(
            rig.InitDB("./testdata/migrations"),
        ),
        rig.Temporal(
            rig.SearchAttributes("OrderID", "Keyword"),
        ),
        rig.Service("order-service",
            rig.Go("./cmd/order-service"),
            rig.Args("-c", "${RIG_TEMP_DIR}/config.json"),
            rig.Egress("database", "postgres"),
            rig.Egress("temporal", "temporal"),
            rig.Prestart(func(ctx context.Context) error {
                w := rig.WiringFromContext(ctx)
                cfg := buildConfig(w.Egresses["database"])
                return os.WriteFile(
                    filepath.Join(w.TempDir, "config.json"), cfg, 0644,
                )
            }),
            rig.Init(func(ctx context.Context) error {
                ep := rig.EndpointFromContext(ctx)
                db = connectTo(ep)
                return nil
            }),
        ),
    ))

    api := resolved.Endpoint("order-service")
    resp, _ := http.Post(fmt.Sprintf("http://%s:%d/orders", api.Host, api.Port), ...)

    var count int
    db.QueryRow("SELECT count(*) FROM orders").Scan(&count)
    assert.Equal(t, 1, count)
}
```

### `rig.Up`

The one-liner that starts an environment, blocks until all services are ready, and registers cleanup with `t.Cleanup`. Returns the resolved environment for endpoint lookups.

Internally, `Up` creates the environment, opens an SSE stream, and runs an event loop that handles callback requests, streams logs, and waits for the environment to be ready. From the caller's perspective, it's a single blocking call.

```go
func Up(t *testing.T, env Environment) *ResolvedEnvironment {
    t.Helper()

    // Ensure rigd is running (download + start if needed)
    serverAddr, err := ensureServer()
    if err != nil {
        t.Fatal("rig: failed to start server:", err)
    }

    // Create environment (returns immediately with ID)
    spec := env.toSpec()
    envID, err := createEnvironment(serverAddr, spec)
    if err != nil {
        t.Fatal("rig: failed to create environment:", err)
    }

    t.Cleanup(func() {
        destroyEnvironment(serverAddr, envID)
    })

    // Open SSE stream and run event loop until environment is up or failed.
    // The event loop handles callback requests, streams logs, and waits for
    // the terminal event (environment.up or environment.failed).
    resolved, err := streamUntilReady(serverAddr, envID, env.inlineFuncs)
    if err != nil {
        t.Fatal("rig:", err)
    }
    return resolved
}

// streamUntilReady opens the SSE event stream for the environment and
// processes events until a terminal event arrives. This is the SDK's
// core runtime — everything flows through this single event loop.
func streamUntilReady(serverAddr, envID string, handlers map[string]HandlerFunc) (*ResolvedEnvironment, error) {
    ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
    defer cancel()

    for event := range streamEvents(ctx, serverAddr, envID) {
        switch event.Type {
        case "callback.request":
            // Dispatch to the registered inline function
            handler := handlers[event.Callback.Name]
            result, err := handler(callbackContext(event))
            // POST the result back to rigd
            postCallbackResult(serverAddr, envID, event.Callback.RequestID, result, err)

        case "environment.up":
            return event.Resolved, nil

        case "environment.failed":
            return nil, fmt.Errorf("environment failed: %s", event.Error)

        case "service.log":
            // Optional: stream service logs to test output
            // t.Logf("%s | %s", event.Service, event.Log.Data)
        }
    }
    return nil, ctx.Err()
}
```

### Server API

```
POST   /environments                         Create environment (returns immediately)
                                              Body: {spec}
                                              Response: {id}

GET    /environments/{id}/events              SSE event stream (lifecycle, callbacks, logs)
                                              The SDK opens this immediately after creating
                                              the environment and reads it until environment.up
                                              or environment.failed

POST   /environments/{id}/callbacks/{req_id}  Post callback result
                                              Body: {data, error}
                                              Called by the SDK when a callback.request
                                              event has been handled

DELETE /environments/{id}                     Destroy environment (blocks until torn down)

GET    /environments/{id}                     Get environment status + resolved endpoints
```

### Callback Protocol

Client-side function execution uses the SSE event stream for requests and regular HTTP POSTs for responses. No callback server, no host discovery, no bidirectional connectivity required.

**Flow:**

1. rigd reaches a `client_func` in the lifecycle
2. rigd publishes a `callback.request` event to the event log (and therefore the SSE stream)
3. rigd calls `WaitFor` on a `callback.response` event with matching `RequestID` — the lifecycle step blocks
4. The SDK reads the `callback.request` from the SSE stream
5. The SDK dispatches to the registered handler function (which can close over test-local state)
6. The SDK POSTs the result to `POST /environments/{id}/callbacks/{request_id}`
7. rigd receives the POST, publishes a `callback.response` event
8. The `WaitFor` matches, the lifecycle step resumes

```
SSE event (server → client) — prestart hook example (includes egresses):
{
    "type": "callback.request",
    "service": "order-service",
    "callback": {
        "request_id": "cb-a1b2c3",
        "name": "write-order-config",
        "type": "hook",
        "wiring": {
            "ingresses": {"default": {"host": "127.0.0.1", "port": 8234, ...}},
            "egresses": {"database": {"host": "127.0.0.1", "port": 54321, ...}},
            "temp_dir": "/tmp/rig/abc123/order-service",
            "attributes": {"RIG_TEMP_DIR": "/tmp/rig/abc123/order-service", ...}
        }
    }
}

// For an init hook, wiring would contain ingresses only — no egresses field.

POST /environments/{id}/callbacks/cb-a1b2c3 (client → server):
{
    "data": {},
    "error": ""
}
```

**Callback timeout**: rigd's `WaitFor` uses a context with a configurable deadline (default 30s). If the deadline is exceeded before a matching `callback.response` arrives, the callback is treated as a failure and the service lifecycle aborts with a clear error identifying the hung callback.

**No host discovery needed**: because the client only makes outbound HTTP requests to rigd (never the reverse), there is no callback host to discover. The SDK works identically whether the test process runs on the host, inside a Docker container, or in any other environment where it can reach rigd.

For custom client-side service types, the same protocol is used with different `type` values in the callback request (`"publish"`, `"start"`, `"ready"`). The SDK's event loop handles them all uniformly.

---

## Project Structure

```
rig/
├── cmd/
│   └── rigd/                     # Server binary
│       └── main.go
├── spec/                          # Shared types (JSON wire format)
│   ├── environment.go             # Environment, Service
│   ├── endpoint.go                # Endpoint, Protocol
│   ├── ingress.go                 # IngressSpec
│   ├── egress.go                  # EgressSpec + single-ingress shorthand
│   ├── hooks.go                   # HookSpec (prestart, init)
│   ├── ready.go                   # ReadySpec
│   ├── status.go                  # ServiceStatus enum
│   └── service.go                 # Service config, type field
├── server/
│   ├── server.go                  # HTTP API server (POST/DELETE/GET /environments, SSE, callback results)
│   ├── orchestrator.go            # Artifact phase → service phase
│   ├── lifecycle.go               # Per-service lifecycle sequence
│   ├── eventlog.go                # Log-based event bus: coordination, observability, callbacks, replay
│   ├── sse.go                     # SSE stream handler (streams event log to clients)
│   ├── ports.go                   # Random port allocator (OS-assigned)
│   ├── wiring.go                  # Attribute → env var mapping + template expansion
│   ├── validate.go                # Spec validation (cycles, refs, types, duplicates)
│   ├── idle.go                    # Idle timeout + auto-shutdown
│   ├── artifact/                  # Artifact resolution + caching
│   │   ├── cache.go               # Content-addressable cache + file locks
│   │   ├── resolver.go            # Parallel resolution with dedup
│   │   ├── docker.go              # Docker image pre-pull
│   │   ├── gobuild.go             # Go module compilation + source hash cache
│   │   └── download.go            # URL download
│   ├── ready/                     # Protocol-aware health checks
│   │   ├── ready.go               # ReadyChecker interface + poll loop
│   │   ├── tcp.go                 # TCP dial
│   │   ├── http.go                # HTTP HEAD
│   │   └── grpc.go                # gRPC health check
│   └── service/                   # Service type implementations
│       ├── type.go                # ServiceType interface
│       ├── registry.go            # Built-in + user-registered types
│       ├── container.go           # Generic Docker container
│       ├── process.go             # Run a binary
│       ├── script.go              # Run a shell script
│       ├── gobuild.go             # Compile + run Go module
│       ├── postgres.go            # Postgres container + PG attributes
│       ├── temporal.go            # Temporal dev server
│       ├── redis.go               # Redis container
│       └── hooks/                 # Built-in hooks
│           ├── initdb.go          # SQL migration runner
│           ├── temporal_ns.go     # Create namespace
│           ├── temporal_sa.go     # Create search attributes
│           └── script.go          # Generic shell script hook
├── client/                        # Go SDK (thin — HTTP client + SSE reader only)
│   ├── client.go                  # HTTP client, Up/Down, ensureServer
│   ├── stream.go                  # SSE stream reader + event loop (callback dispatch, log streaming)
│   ├── builder.go                 # rig.Postgres(), rig.Go(), rig.Egress(), etc.
│   ├── handler.go                 # Inline func registration
│   ├── server.go                  # rigd auto-management (download, cache, start)
│   └── type.go                    # Client-side ServiceType (escape hatch)
├── testdata/                      # Test fixtures for end-to-end tests
│   ├── services/                  # Small test binaries for integration tests
│   │   ├── echo/                  # Minimal HTTP echo server
│   │   └── greet/                 # Minimal gRPC greeter server
│   ├── specs/                     # Example environment specs (JSON)
│   └── migrations/                # SQL migrations for postgres integration tests
├── go.mod
└── go.sum
```

---

## Build Plan

### Phases 1–7 — COMPLETE

Core architecture is built and tested:
- **Phase 1** — Spec types + validation (`spec/`, `server/validate.go`)
- **Phase 2** — Core infrastructure (event log, ports, wiring)
- **Phase 3** — Lifecycle orchestration + health checks (`server/lifecycle.go`, `server/ready/`)
- **Phase 4** — HTTP API + SSE + end-to-end tests (`server/server.go`, `server/sse.go`)
- **Phase 5** — Artifact system with caching and dedup (`server/artifact/`)
- **Phase 6** — Go client SDK (`client/`) — note: `ensureServer` auto-management deferred to Phase 11
- **Phase 7** — Docker container service type (`server/service/container.go`)

Also shipped: transparent HTTP/TCP proxy (`server/proxy/`), split event log optimization, benchmarks.

### Phase 8 — Builtin Service Types (Postgres complete, Temporal + Redis remaining)

**Done:**
- `server/service/postgres.go` — container lifecycle, `pg_isready` health check, PG attribute publishing, SQL init hooks via `docker exec psql`
- Client API: `rig.Postgres().InitSQL(...).InitHook(fn)`

**Remaining:**
- `server/service/temporal.go` — Temporal dev server. Requires the Download artifact resolver (binary, not container). Publishes `TEMPORAL_ADDRESS` and `TEMPORAL_NAMESPACE`. Custom ready check against gRPC health endpoint. Init hooks: `create-namespace`, `create-search-attributes`
- `server/service/redis.go` — Redis container. Publishes `REDIS_URL`. Straightforward container wrapper
- `server/artifact/download.go` — Download resolver. Fetch URL to local path, cached by URL + checksum. Needed by Temporal (and future binary-distributed services)

### Phase 9 — Generic `exec` Init Hook

Most cloud services in test environments are containers with a CLI for setup (create topic, create bucket, create queue). Rather than building a server-side type for each one, a generic `exec` hook lets users define init commands declaratively:

```json
"hooks": {
  "init": [{"type": "exec", "config": {"command": ["kafka-topics", "--create", "--topic", "orders"]}}]
}
```

This runs `docker exec` into the running container after it's healthy. The server already does this for Postgres's `psql` execution — this generalises it to any container service.

**Why this matters:** unlocks Kafka, LocalStack, Elasticsearch, RabbitMQ, and any container-with-a-CLI from any SDK language, entirely declaratively. No client-side code, no callback round-trips. Reduces the pressure on both new builtin types and client-side custom types.

**Implementation:**
- Extract the `docker exec` logic from Postgres into a shared `execInContainer` helper
- Add `exec` as a recognised hook type on the Container service type's `Initializer`
- Postgres's SQL hook becomes a thin wrapper that builds the `psql` command and delegates to `execInContainer`

### Phase 10 — Diagnostics & Debugging

**Progress watchdog:** Track the last time any service changed lifecycle phase. If no progress for 30 seconds, log a diagnostic snapshot: which services are blocked, in which phase, what they're waiting on, and the current status of their dependencies. The watchdog does not abort — it only logs. The environment startup timeout remains the hard boundary.

**`RIG_PRESERVE_ON_FAILURE`:** When a test fails, preserve temp directories, log files, and state instead of cleaning them up. The Go SDK checks `t.Failed()` in the cleanup function. Set `RIG_PRESERVE=true` to preserve on every run regardless of outcome.

**Timeout diagnostics:** When any timeout fires, include which service is stuck, which lifecycle phase it's in, and what it's waiting on. Example: `"environment startup timeout (2m0s): service 'order-service' stuck in WAIT_FOR_EGRESSES — waiting on 'postgres' (status: starting)"`

### Phase 11 — `rigd` Binary & Server Auto-Management

**Why this is on the critical path:** The target use case is Java apps using Temporal, Postgres, and cloud services. Java tests need a Java SDK, which requires `rigd` as a standalone HTTP server. The in-process Go model works today but doesn't extend to other languages.

**`cmd/rigd/main.go`:**
- Starts the HTTP server on a random port
- Writes listen address to `~/.rig/rigd-<hash>.addr`
- Removes address file on shutdown
- Idle timeout shuts down after no active environments for 5 minutes
- Signal handling (SIGINT/SIGTERM) for graceful shutdown

**`ensureServer` in Go SDK (`client/server.go`):**
1. Check for running `rigd` via address file `~/.rig/rigd-<hash>.addr`
2. If found, probe with HTTP health check to confirm liveness
3. If not running, check `~/.rig/bin/<hash>/rigd` for cached binary
4. If not cached, download the correct binary for the platform
5. Acquire per-hash file lock (`~/.rig/rigd-<hash>.lock`) to prevent races
6. Start `rigd` as a detached background process
7. Release lock — other processes waiting on it find `rigd` running at step 1

The SDK embeds a content hash of the compatible `rigd` version. Multiple engine versions coexist — each writes to its own address file. See the Versioning Model section above.

### Phase 12 — Java SDK

Thin client: HTTP client + SSE stream reader + spec builder. No server, no orchestration, no port allocation.

- **Spec builder** — fluent Java API that produces the same JSON spec the Go SDK produces
- **SSE stream reader** — reads lifecycle events, dispatches callback requests to registered handlers
- **Callback dispatch** — for `client_func` hooks (prestart/init closures defined in test code)
- **Test framework integration** — JUnit 5 extension that calls `Up()` before test, `Down()` after, registers cleanup
- **`ensureServer`** — same protocol as Go SDK (address file, file lock, download, start)

The Java SDK should be a few hundred lines — the server does all the real work.

### Phase 13 — Polish

- Godoc on all public API surfaces in `client/`
- Example test files demonstrating common patterns (postgres + service, temporal workflow, multi-service graph)
- Error message quality review across all phases
- `sc.emit()` refactor — typed helper methods on `serviceContext` to reduce Event construction boilerplate in lifecycle.go

---

## Reuse of `matgreaves/run`

The [`run`](https://github.com/matgreaves/run) library provides several utilities beyond the core concurrency primitives that we should build on directly rather than reinventing.

### Core Primitives (already covered above)

- `Runner` interface, `Func`, `Sequence`, `Group`, `Once`, `Go`, `Idle`
- `Start` / `Ready` — start a long-lived process and wait for readiness

### `run.Process` — External Process Management

`run.Process` wraps `exec.Cmd` with:
- Process group management (`Setpgid`) so child processes are cleaned up together
- Graceful shutdown via `SIGINT` on context cancellation
- Forceful cleanup via `onexit.Kill` with `SIGKILL` if the parent dies abruptly
- `Stdout` and `Stderr` as `io.Writer` fields — this is the hook point for centralised logging

We should use `run.Process` as the foundation for every service type that runs an external binary (process, go-build, script, temporal, etc.) rather than calling `exec.Cmd` directly. This gives us consistent signal handling and log capture across all service types.

### `exp.HTTPServer` / `exp.StartHTTPServer` — HTTP Server Runner

Wraps `http.Server` as a `Runner` with graceful shutdown. Useful for rigd's own builtin services:

- **File server** — serve files from a service's temp dir or a shared workspace over HTTP. Useful when a service needs to fetch config/artifacts over HTTP rather than reading from disk
- **HTTP reverse proxy** — a builtin service type that proxies to another service's ingress. Useful for adding auth, TLS termination, or request logging in front of a service during testing
- **rigd's own API** — if we want an HTTP API alongside gRPC (e.g. for a future web UI / dashboard)

### `exp.Poller` — HTTP Health Check with Exponential Backoff

The `Poller` and `pokeHTTP` pattern is exactly what our `server/ready/http.go` should build on:
- Exponential backoff with configurable initial/max intervals
- Raw TCP + HTTP probe (sends `OPTIONS *`, checks for non-502/504 response)
- Returns a `Runner` that blocks until ready — composes naturally with `Sequence`

We should generalise this pattern into a `poll` function that our TCP, HTTP, and gRPC ready checkers all use, keeping the exponential backoff logic in one place.

### `exp/ports` — Random Port Allocation

Our `server/ports.go` uses the same approach as `exp/ports` — bind to `:0`, let the OS assign a free port, close the listener, return the port. This is the entire port allocation strategy. No configured ranges, no cross-process file locks, no coordination protocol. The OS is the allocator.

On top of this, rigd adds lightweight in-process tracking:
- Map of allocated port → environment instance ID (prevents the same rigd from handing out a port it already assigned to an active environment)
- Release on environment teardown (removes ports from the tracking map)

### `onexit` — Robust Cleanup on Abrupt Exit

`onexit.Killer` runs cleanup commands via a detached shell script that survives even `SIGKILL` of the parent process. This is the safety net that prevents resource leaks when rigd or the test process dies uncleanly (user spamming ctrl+c, OOM kill, CI timeout, etc.).

Every resource rigd creates should register a corresponding cleanup with `onexit`:

- **Containers**: `docker rm -f <container_id>` — prevents orphaned containers accumulating on the host
- **Process groups**: `kill -9 -<pgid>` — `run.Process` already does this internally, so all process-based services get it for free
- **Temp directories**: `rm -rf <temp_dir>` — prevents disk space leaks from abandoned service workspaces

The pattern for every resource:

```go
// Start container
containerID := startContainer(...)

// Register cleanup that survives our death
cancel, _ := onexit.OnExitF("docker rm -f %s", containerID)

// On graceful teardown, clean up normally and deregister the safety net
defer func() {
    stopContainer(containerID)
    cancel() // don't run the onexit command, we handled it
}()
```

This means even in the worst case — rigd receives SIGKILL mid-test — the detached shell script cleans up every container, kills every process group, and removes every temp directory. No resource leaks, no manual cleanup, no stale containers clogging CI machines.

For environments with many services, the onexit registrations accumulate but each is a simple shell one-liner. The script runs them all in parallel on exit for fast cleanup.

---

## Centralised Logging

Service logs should be captured, prefixed, and surfaced through the event bus so that all service output is observable from a single place.

### Log Capture via `run.Process`

`run.Process` exposes `Stdout` and `Stderr` as `io.Writer` fields. We inject a `ServiceLogger` that:

1. **Prefixes** each line with the service name and stream (stdout/stderr)
2. **Writes** to a log file in the service's temp dir (for post-mortem debugging)
3. **Publishes** log lines to the event bus as events (for real-time streaming to clients)

```go
type ServiceLogger struct {
    serviceName string
    stream      string    // "stdout" or "stderr"
    bus         *EventBus
    file        io.Writer // log file in RIG_TEMP_DIR
}

func (l *ServiceLogger) Write(p []byte) (n int, err error) {
    // Write to file for persistence
    l.file.Write(p)

    // Publish to event bus for real-time observability
    l.bus.Publish(Event{
        Type:    EventServiceLog,
        Service: l.serviceName,
        Log: &LogEntry{
            Stream: l.stream,
            Data:   string(p),
        },
    })
    return len(p), nil
}
```

### Wiring Into Process-Based Services

When building a `run.Process` for any service, the orchestrator wires up the logger:

```go
proc := run.Process{
    Name:   serviceName,
    Path:   binaryPath,
    Args:   expandedArgs,
    Env:    envVars,
    Stdout: NewServiceLogger(serviceName, "stdout", bus, logFile),
    Stderr: NewServiceLogger(serviceName, "stderr", bus, logFile),
}
```

### Container Logs

For Docker containers, we attach to the container's log stream and pipe through the same `ServiceLogger`. This means container output and process output are handled identically from the event bus's perspective.

### Log Events

`EventServiceLog` and `LogEntry` are defined in the Event Types section above. The log event is a first-class event on the bus, not a separate system.

Subscribers can:
- **Print to terminal** — prefixed with service name and coloured by stream
- **Stream to client** — via the SSE event stream, so the test suite can see service logs in real time
- **Write to aggregate log file** — single file with interleaved, timestamped output from all services
- **Filter** — subscribe to logs from a specific service only

### Log Files on Disk

Each service's temp dir contains its logs:

```
$RIG_TEMP_DIR/
├── stdout.log
├── stderr.log
└── ... (config files, artifacts, etc.)
```

On environment teardown these are cleaned up unless preservation is enabled (see Debugging Support below).

---

## Builtin Server-Side Utilities

Beyond service types, the server can provide utility runners built on `run` that are useful as building blocks within environments.

### File Server

An HTTP file server backed by `exp.HTTPServer` that serves files from a directory. Useful as a builtin service type when services need to fetch configuration or test fixtures over HTTP:

```json
{
  "config-server": {
    "type": "file-server",
    "config": {"root": "./testdata/config"}
  }
}
```

Internally this is just `exp.HTTPServer(http.FileServer(http.Dir(root)), allocatedAddr)`.

### HTTP Reverse Proxy

A reverse proxy service backed by `exp.HTTPServer` + `httputil.ReverseProxy`. Sits in front of another service's ingress. Use cases:

- Add request/response logging for debugging
- Simulate auth middleware
- Inject headers or latency for chaos testing
- TLS termination with test certificates

```json
{
  "api-proxy": {
    "type": "http-proxy",
    "config": {"target_header_prefix": "X-Test-"},
    "egresses": {"target": {"service": "order-service"}},
    "ingresses": {"default": {"protocol": "http"}}
  }
}
```

Services that depend on the API go through the proxy instead of directly to the service — the wiring handles this naturally via egress references.

---

## Spec Validation

Before the artifact phase begins, the environment spec is validated in a single pass. Validation catches structural errors early with clear, actionable messages instead of confusing failures mid-lifecycle.

### Validation Rules

1. **Unknown service types** — reject types not in the built-in registry and not declared as client-side custom types
2. **Egress references** — every egress must reference an existing service; if an ingress name is specified, that ingress must exist on the target service
3. **Self-referencing egresses** — a service cannot have an egress to itself
4. **Cycle detection** — walk the egress dependency graph and reject cycles with a clear error listing the cycle path (e.g., `"cycle detected: A → B → C → A"`)
5. **Duplicate names** — no duplicate service names, no duplicate ingress names within a service, no duplicate egress names within a service
6. **Invalid protocol values** — protocol must be one of `tcp`, `http`, `grpc`
7. **Container port required** — container-type services must specify `container_port` on each ingress
8. **Single-ingress shorthand resolution** — egresses that omit the ingress name are resolved here; error if the target service has multiple ingresses

Validation runs on every `POST /environments` request before any resources are allocated. The response includes all validation errors (not just the first), so the user can fix them in one pass.

---

## Timeout Configuration

Timeouts prevent hung environments from blocking test suites indefinitely. Every timeout has a sensible default and is configurable via the environment spec or server config.

### Timeout Defaults

| Timeout | Default | Configurable Via |
|---------|---------|-----------------|
| Environment startup (total) | 2 minutes | `RIG_STARTUP_TIMEOUT` / spec field |
| Individual service health check | 60 seconds | `ReadySpec.Timeout` per ingress |
| Callback response | 30 seconds | `RIG_CALLBACK_TIMEOUT` |
| Artifact resolution (total) | 5 minutes | `RIG_ARTIFACT_TIMEOUT` |
| Graceful shutdown | 10 seconds | `RIG_SHUTDOWN_TIMEOUT` |
| Server idle (auto-shutdown) | 5 minutes | `RIG_IDLE_TIMEOUT` |

### Timeout Diagnostics

When any timeout fires, the error message includes:
- Which timeout fired and its configured duration
- Which service(s) are stuck and in which lifecycle phase
- For egress waits: which upstream service(s) the stuck service is waiting on and their current status
- Suggestion to increase the timeout if the operation is legitimately slow

Example: `"environment startup timeout (2m0s): service 'order-service' stuck in WAIT_FOR_EGRESSES — waiting on 'postgres' (status: starting)"`

---

## Error Responses

When environment creation fails, the `POST /environments` response includes structured error information so the client can present actionable diagnostics:

```json
{
    "error": "service 'order-service' failed during START",
    "failed_service": "order-service",
    "failed_phase": "start",
    "detail": "exit code 1: cannot bind to port 8080",
    "logs_tail": [
        "2024-01-15T10:30:01Z order-service | Error: address already in use",
        "2024-01-15T10:30:01Z order-service | exit status 1"
    ],
    "services_status": {
        "postgres": "ready",
        "temporal": "ready",
        "order-service": "failed"
    }
}
```

For validation errors (caught before any resources are allocated):

```json
{
    "error": "spec validation failed",
    "validation_errors": [
        "egress 'database' on service 'order-service' references unknown service 'postgre' (did you mean 'postgres'?)",
        "cycle detected: order-service → payment-service → order-service"
    ]
}
```

The SDK translates these into language-appropriate error types. The Go SDK formats them as multi-line error messages suitable for `t.Fatal`.

---

## Container Networking

All inter-service communication goes through host-mapped ports. This is an explicit design decision:

- **Consistency** — process-based and container-based services use the same addressing model. An egress to a container looks identical to an egress to a process.
- **Simplicity** — no Docker networks to create, manage, or clean up. No DNS resolution inside containers.
- **Debuggability** — every endpoint is reachable from the host, so `curl`, `psql`, and other tools work directly.
- **Template expansion** — `${HOST}` and `${PORT}` variables work the same way regardless of whether the target is a container or a process.

Container services bind their internal ports to host-allocated ports via Docker's `-p` flag. Services communicate via `127.0.0.1:<allocated_port>`, never via container names or Docker DNS.

Future consideration: for high-throughput container-to-container testing where host port mapping is a bottleneck, a Docker network mode could be added as an opt-in. This would create a per-environment Docker network, assign container hostnames, and adjust wiring accordingly. Not needed for v1.

---

## Shared Environment State

Two levels of temporary directories provide isolation with an escape hatch for sharing:

- **`RIG_TEMP_DIR`** — per-service temp directory. Each service gets its own isolated workspace for config files, logs, and artifacts. This is the default and covers most use cases.
- **`RIG_ENV_DIR`** — per-environment shared directory. All services in the same environment instance can read/write to this directory. Use cases: a prestart hook on service A writes a config file that service B reads, shared test fixtures, inter-service coordination files.

Both are created before the service phase and cleaned up on teardown (unless preservation is enabled).

---

## Debugging Support

When tests fail, preserving state is critical for debugging. rig provides several mechanisms:

### Preserve on Failure

Set `RIG_PRESERVE_ON_FAILURE=true` to keep all temp directories, log files, and state when an environment teardown is triggered after a test failure. The Go SDK checks `t.Failed()` in the cleanup function:

```go
t.Cleanup(func() {
    if t.Failed() && os.Getenv("RIG_PRESERVE_ON_FAILURE") == "true" {
        t.Logf("rig: preserving environment state for debugging:")
        t.Logf("  environment dir: %s", resolved.EnvDir)
        for name, svc := range resolved.Services {
            t.Logf("  %s logs: %s/stdout.log, %s/stderr.log", name, svc.TempDir, svc.TempDir)
        }
        destroyEnvironment(serverAddr, resolved.ID) // still stop services, just keep files
        return
    }
    destroyEnvironmentAndCleanup(serverAddr, resolved.ID)
})
```

### Always-Preserve Mode

Set `RIG_PRESERVE=true` to keep state on every run, regardless of test outcome. Useful during active development.

---

## Dependencies

- [`github.com/matgreaves/run`](https://github.com/matgreaves/run) — concurrency primitives (`Group`, `Sequence`, `Process`, `Start`/`Ready`), HTTP server/poller, port allocation, `onexit` cleanup
- `net/http` (stdlib) — HTTP API server + SSE streaming (no external dependency needed)
- Docker SDK (`github.com/docker/docker`) — container management
- `github.com/matryer/is` — test assertions (consistent with `run`)

See [ANALYSIS.md](./ANALYSIS.md) for a detailed comparative analysis against testcontainers, Docker Compose, Dagger, Tilt, and dockertest.

See [VISION.md](./VISION.md) for the long-term vision: transparent proxy layer, live dashboard, session recording & replay, and chaos/fault injection.

