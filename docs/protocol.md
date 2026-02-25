# rigd Wire Protocol Reference

Reference for building rig client SDKs in any language. Covers the HTTP API, JSON spec format, SSE event stream, callback protocol, and wiring conventions.

The Go SDK (`github.com/matgreaves/rig/client`) is the reference implementation.

## HTTP Endpoints

### `GET /health`

Returns `200` with `{"status":"ok"}`. Use to verify the server is running.

### `POST /environments`

Creates an environment. Orchestration runs asynchronously — the response returns immediately with an instance ID. Connect to the SSE stream to track progress.

**Request**: JSON body (see [Spec Format](#spec-format))

**Response**: `201 Created`
```json
{"id": "a1b2c3d4e5f6"}
```

**Errors**:
- `400` — malformed JSON: `{"error": "decode: ..."}`
- `422` — validation failure: `{"error": "spec validation failed", "validation_errors": ["..."]}`
- `500` — orchestration failure: `{"error": "orchestrate: ..."}`

### `GET /environments/{id}/events`

SSE event stream. Replays all events from the beginning (or from `Last-Event-ID` for reconnection), then streams new events as they occur.

**Headers**: Optional `Last-Event-ID: <seq>` to resume from a specific sequence number.

**Response**: `200` with `Content-Type: text/event-stream`

```
id: 1
event: service.starting
data: {"seq":1,"type":"service.starting","service":"api","timestamp":"..."}

id: 2
event: service.healthy
data: {"seq":2,"type":"service.healthy","service":"api","timestamp":"..."}

```

Each frame has: `id` (sequence number), `event` (type string), `data` (full Event JSON), blank line.

`service.log` events are filtered out of the SSE stream (high volume). They're available via `GET /environments/{id}/log`.

### `GET /environments/{id}`

Returns the current resolved state of the environment.

**Response**: `200`
```json
{
  "id": "a1b2c3d4e5f6",
  "name": "TestMyApp",
  "services": {
    "api": {
      "ingresses": {
        "default": {"host": "127.0.0.1", "port": 54321, "protocol": "http", "attributes": {}}
      },
      "egresses": {
        "db": {"host": "127.0.0.1", "port": 54322, "protocol": "tcp", "attributes": {...}}
      },
      "status": "ready"
    }
  }
}
```

Service status values: `pending`, `starting`, `healthy`, `ready`, `failed`, `stopping`, `stopped`.

### `GET /environments/{id}/log`

Returns the full event log as a JSON array (including `service.log` events).

**Response**: `200` with `[{event}, {event}, ...]`

### `POST /environments/{id}/events`

Client-to-server event channel. Used for callback responses, error reporting, log forwarding, and test assertions.

**Request**: JSON body with `type` field determining behavior.

**Response**: `204 No Content` on success, `400` on unknown type, `404` on unknown environment.

See [Client Events](#client-events).

### `DELETE /environments/{id}`

Tears down the environment. Cancels all services, waits for cleanup, releases ports.

**Query parameters**:
- `preserve=true` — keep environment temp directory after teardown
- `reason=test_failed` — signal why teardown was requested (affects log outcome)
- `log=true` — write event log files to disk

**Response**: `200`
```json
{
  "id": "a1b2c3d4e5f6",
  "status": "destroyed",
  "env_dir": "/tmp/rig/a1b2c3d4e5f6",
  "log_file": "~/.rig/logs/TestMyApp-a1b2c3d4e5f6.jsonl",
  "log_file_pretty": "~/.rig/logs/TestMyApp-a1b2c3d4e5f6.log"
}
```

`log_file` and `log_file_pretty` are only present when `log=true` and writing succeeds.

---

## Spec Format

The JSON body sent to `POST /environments`.

```json
{
  "name": "TestMyApp",
  "services": {
    "db": {
      "type": "postgres",
      "config": {"image": "postgres:16"},
      "hooks": {
        "init": [{"type": "initdb", "config": {"sql": "CREATE TABLE users (...)"}}]
      }
    },
    "api": {
      "type": "go",
      "config": {"module": "./cmd/api"},
      "args": ["--verbose"],
      "ingresses": {
        "default": {"protocol": "http", "ready": {"path": "/health"}}
      },
      "egresses": {
        "db": {"service": "db"}
      },
      "hooks": {
        "prestart": [{"type": "client_func", "client_func": {"name": "seed_data"}}]
      }
    }
  },
  "observe": true
}
```

### Environment

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Environment identifier (typically the test name) |
| `services` | object | Yes | Map of service name to service spec. At least one required. |
| `observe` | boolean | No | Enable transparent traffic proxying. Default `false` at protocol level; Go SDK defaults to `true`. |

### Service

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | Service implementation: `container`, `go`, `process`, `postgres`, `temporal`, `client` |
| `config` | object | No | Type-specific configuration as raw JSON |
| `args` | string[] | No | Command-line arguments. Supports `${VAR}` template expansion. |
| `ingresses` | object | No | Map of ingress name to IngressSpec. If omitted, a single HTTP ingress named `"default"` is implied. |
| `egresses` | object | No | Map of egress name to EgressSpec |
| `hooks` | object | No | Lifecycle hooks (`prestart`, `init` arrays) |

### IngressSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `protocol` | string | Yes | `"tcp"`, `"http"`, `"grpc"`, or `"kafka"` |
| `container_port` | integer | No | Fixed port inside container. Required for container service types. |
| `ready` | object | No | Health check override (see ReadySpec). Inferred from protocol if omitted. |
| `attributes` | object | No | Static attributes published with the endpoint |

### EgressSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `service` | string | Yes | Target service name |
| `ingress` | string | No | Target ingress name. Defaults to sole ingress if target has only one; validation fails if target has multiple and this is omitted. |

### ReadySpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | No | Health check type: `"tcp"`, `"http"`, `"grpc"`. Defaults to ingress protocol. |
| `path` | string | No | HTTP GET path. Default `"/"`. |
| `interval` | string | No | Poll interval as duration string (e.g. `"100ms"`). Default `"100ms"` with backoff. |
| `timeout` | string | No | Max wait as duration string (e.g. `"30s"`). Default from startup timeout. |

Duration strings use Go's `time.ParseDuration` format: `"5s"`, `"100ms"`, `"1m30s"`, `"500us"`.

### HookSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | Hook implementation type (see below) |
| `client_func` | object | No | For `type: "client_func"`: `{"name": "handler_name"}` |
| `config` | object | No | Type-specific configuration |

Hook types:
- `"client_func"` — callback to client-side function (works in prestart and init)
- `"initdb"` — Postgres: run SQL via `docker exec` (config: `{"sql": "..."}` or `{"sql_dir": "path"}`)
- `"create-namespace"` — Temporal: create namespace (config: `{"namespace": "..."}`)

### Hooks

| Field | Type | Description |
|-------|------|-------------|
| `prestart` | HookSpec[] | Run after egresses are resolved, before the process starts. Receives full wiring. |
| `init` | HookSpec[] | Run after health checks pass, before the service is marked ready. Receives ingress wiring only. |

### Service type configs

Each service type reads type-specific fields from `config`:

**`go`**: `{"module": "./cmd/api", "goos": "linux", "goarch": "amd64"}`
**`container`**: `{"image": "redis:7"}`
**`process`**: `{"path": "/usr/local/bin/myservice"}`
**`postgres`**: `{"image": "postgres:16"}`
**`temporal`**: `{"version": "1.5.1"}`
**`client`**: no config (lifecycle managed entirely via callbacks)

---

## Endpoint

Resolved at runtime by the server. Never appears in the spec — only in events and responses.

```json
{
  "host": "127.0.0.1",
  "port": 54321,
  "protocol": "http",
  "attributes": {
    "PGHOST": "127.0.0.1",
    "PGPORT": "54321",
    "PGUSER": "postgres",
    "PGPASSWORD": "postgres",
    "PGDATABASE": "test_abc123"
  },
  "address_attrs": {
    "PGHOST": "host",
    "PGPORT": "port"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `host` | string | Hostname or IP |
| `port` | integer | Port number |
| `protocol` | string | `"tcp"`, `"http"`, `"grpc"`, `"kafka"` |
| `attributes` | object | Key-value attributes (typed as `any` — strings, numbers, booleans) |
| `address_attrs` | object | Declares which attributes derive from the address (`"host"`, `"port"`, `"hostport"`). Used by the proxy layer to rewrite attributes when the address changes. |

Well-known attributes published by built-in service types:

| Service | Attributes |
|---------|-----------|
| Postgres | `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE` |
| Temporal | `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE` |

---

## Event Types

Every event has this base structure:

```json
{
  "seq": 42,
  "type": "service.ready",
  "environment": "TestMyApp",
  "timestamp": "2025-02-25T10:30:45.123456789Z",
  "service": "api",
  "error": "",
  ...
}
```

| Field | Type | Present on |
|-------|------|-----------|
| `seq` | uint64 | All events |
| `type` | string | All events |
| `environment` | string | All events |
| `timestamp` | RFC3339Nano | All events |
| `service` | string | Service-scoped events |
| `ingress` | string | `ingress.published`, `proxy.published` |
| `endpoint` | Endpoint | `ingress.published`, `proxy.published` |
| `artifact` | string | Artifact events |
| `error` | string | Failure events |
| `log` | LogEntry | `service.log` |
| `callback` | CallbackRequest | `callback.request` |
| `result` | CallbackResponse | `callback.response` |
| `request` | RequestInfo | `request.completed` |
| `connection` | ConnectionInfo | `connection.opened`, `connection.closed` |
| `grpc_call` | GRPCCallInfo | `grpc.call.completed` |
| `diagnostic` | DiagnosticSnapshot | `progress.stall` |
| `ingresses` | object | `environment.up` |
| `env_dir` | string | `environment.up` |
| `message` | string | `environment.down`, `progress.stall` |

### Artifact phase

| Type | Description |
|------|-------------|
| `artifact.started` | Artifact resolution began (cache miss) |
| `artifact.completed` | Artifact resolved successfully |
| `artifact.cached` | Artifact loaded from cache (no work needed) |
| `artifact.failed` | Artifact resolution failed. `error` field has details. |

### Service lifecycle

| Type | Description |
|------|-------------|
| `ingress.published` | Endpoint allocated. `ingress` and `endpoint` fields populated. |
| `wiring.resolved` | All egress dependencies resolved for this service. |
| `service.prestart` | Prestart hooks starting. |
| `service.starting` | Process launching. |
| `service.healthy` | Health checks passed. |
| `service.init` | Init hooks starting. |
| `service.ready` | Service ready for traffic. |
| `service.failed` | Service crashed or hook failed. `error` field has details. |
| `service.stopping` | Service shutting down (normal). |
| `service.stopped` | Service exited. |
| `service.log` | Stdout/stderr output. `log` field: `{"stream": "stdout"|"stderr", "data": "..."}`. Not sent over SSE. |

### Callbacks

| Type | Description |
|------|-------------|
| `callback.request` | Server needs client to execute a function. `callback` field populated. |
| `callback.response` | Client's response to a callback. `result` field populated. |

### Environment lifecycle

| Type | Description |
|------|-------------|
| `environment.up` | All services ready. `ingresses` field has the full endpoint map. |
| `environment.failing` | First failure detected. `error` and optionally `service` populated. |
| `environment.destroying` | DELETE received (normal teardown). |
| `environment.down` | Environment shut down. `message` field has failure summary (empty for clean shutdown). |

### Diagnostics

| Type | Description |
|------|-------------|
| `health.check_failed` | A health check probe failed (retrying). |
| `progress.stall` | No progress for 30s. `diagnostic` field has per-service state snapshot. |
| `test.note` | Test assertion or diagnostic from client. `error` field has the message. |

### Traffic observation (when `observe: true`)

| Type | Description |
|------|-------------|
| `proxy.published` | Proxy endpoint allocated for an ingress. |
| `request.completed` | HTTP request/response pair observed. |
| `connection.opened` | TCP connection opened. |
| `connection.closed` | TCP connection closed. |
| `grpc.call.completed` | gRPC call completed. |

---

## Callback Protocol

The callback protocol enables client-side code execution (hooks and Func services) via the SSE event stream.

### Flow

1. Server publishes `callback.request` via SSE:

```json
{
  "type": "callback.request",
  "service": "api",
  "callback": {
    "request_id": "a1b2c3-api-seed_data",
    "name": "seed_data",
    "type": "hook",
    "wiring": {
      "ingresses": {"default": {"host": "127.0.0.1", "port": 54321, "protocol": "http"}},
      "egresses": {"db": {"host": "127.0.0.1", "port": 54322, "protocol": "tcp", "attributes": {...}}},
      "temp_dir": "/tmp/rig/a1b2c3/api",
      "env_dir": "/tmp/rig/a1b2c3"
    }
  }
}
```

2. Client matches `callback.name` to a registered handler, executes it with the provided wiring.

3. Client posts response to `POST /environments/{id}/events`:

```json
{
  "type": "callback.response",
  "request_id": "a1b2c3-api-seed_data",
  "error": ""
}
```

Set `error` to a non-empty string to fail the hook.

### Callback types

| `type` | Behavior | Response timing |
|--------|----------|-----------------|
| `hook` | Execute synchronously, then respond. | After handler returns. |
| `start` | Launch asynchronously, respond immediately. | Immediately (success). Post `service.error` if it fails later. |
| `publish` | Respond with endpoint data. | After publishing. |
| `ready` | Respond after service is ready. | After ready. |

### Timeout

The server waits **30 seconds** for a callback response. If no response arrives, the service fails with: `"callback 'name' response not received within 30s — client may have disconnected"`.

---

## Client Events

Events the client posts to `POST /environments/{id}/events`.

### `callback.response`

```json
{
  "type": "callback.response",
  "request_id": "...",
  "error": "",
  "data": {}
}
```

### `service.error`

Marks a client-side service as failed. Triggers environment teardown.

```json
{
  "type": "service.error",
  "service": "api",
  "error": "handler crashed: panic in user code"
}
```

### `service.log`

Forwards log output from a client-side service.

```json
{
  "type": "service.log",
  "service": "api",
  "stream": "stdout",
  "log_data": "listening on :8080"
}
```

`stream` defaults to `"stdout"` if omitted.

### `test.note`

Records a test assertion or diagnostic message.

```json
{
  "type": "test.note",
  "error": "myapp_test.go:42: expected 200 but got 500"
}
```

---

## Wiring Environment Variables

Services receive their wiring as environment variables. The structured `RIG_WIRING` JSON is the preferred method; flat env vars are a convenience fallback.

### Service-level variables

| Variable | Value |
|----------|-------|
| `RIG_WIRING` | Full wiring as JSON: `{"ingresses":{...},"egresses":{...},"temp_dir":"...","env_dir":"..."}` |
| `RIG_TEMP_DIR` | Per-service temp directory |
| `RIG_ENV_DIR` | Per-environment shared directory |
| `RIG_SERVICE` | Service name |

### Ingress variables

The **default** ingress (named `"default"`) is unprefixed:

```
HOST=127.0.0.1
PORT=54321
```

Named ingresses are prefixed with the uppercased ingress name:

```
METRICS_HOST=127.0.0.1
METRICS_PORT=9090
```

All endpoint attributes are included (e.g. `PGUSER=postgres`, `PGDATABASE=test_abc`).

### Egress variables

Always prefixed by the uppercased egress name:

```
DB_HOST=127.0.0.1
DB_PORT=54322
DB_PGUSER=postgres
DB_PGDATABASE=test_abc
```

### Naming convention

- Hyphens → underscores: `order-db` → `ORDER_DB_`
- Uppercased
- Trailing underscore on prefix

### Template expansion

Service `args` support `${VAR}` expansion against the full env var map:

```json
"args": ["--config=${RIG_TEMP_DIR}/config.json", "--db=${DB_HOST}:${DB_PORT}"]
```

---

## Directory Structure & Server Management

### Rig directory

All rig state lives under a single base directory. The default is `~/.rig`. Override with the `RIG_DIR` environment variable. If `$HOME` is unavailable, falls back to `$TMPDIR/rig`.

```
~/.rig/                          # base directory (or $RIG_DIR)
├── bin/
│   └── v0.2.0/
│       └── rigd                 # downloaded binary for this version
├── cache/                       # artifact cache (Docker images, Go builds, downloads)
├── logs/                        # JSONL event logs and pretty-printed logs per test
│   ├── TestMyApp-a1b2c3.jsonl
│   └── TestMyApp-a1b2c3.log
├── tmp/                         # per-environment temp dirs (cleaned on teardown)
│   └── a1b2c3d4e5f6/           # environment instance ID
│       ├── api/                 # per-service temp dir
│       └── db/
├── rigd.addr                    # server address (unversioned, used with RIG_BINARY)
├── rigd-v0.2.0.addr             # server address (versioned, used with managed binaries)
├── rigd.lock                    # startup lock (unversioned)
├── rigd-v0.2.0.lock             # startup lock (versioned)
└── rigd.log                     # server stderr log
```

### Binary resolution

SDKs embed a version string (e.g. `"0.2.0"`) identifying the `rigd` release they target. The binary search order is:

1. **`RIG_BINARY` env var** — explicit path to a `rigd` binary. Used for development and CI where `make build` produces the binary. If set but the file doesn't exist, fail immediately.
2. **`{rigDir}/bin/v{version}/rigd`** — versioned managed path. This is where auto-download places binaries.
3. **`{rigDir}/bin/rigd`** — legacy unversioned path (backwards compatibility).
4. **`rigd` in `$PATH`** — system-installed binary.
5. **Auto-download** — download from GitHub Releases, extract, and place at `{rigDir}/bin/v{version}/rigd`.

### Auto-download

When no binary is found, download from:

```
https://github.com/matgreaves/rig/releases/download/rigd/v{version}/rigd-{os}-{arch}.tar.gz
```

Where `{os}` is `linux` or `darwin` and `{arch}` is `amd64` or `arm64`.

The archive contains a single `rigd` binary. Extract it to `{rigDir}/bin/v{version}/rigd`. Use a temp file + rename for atomicity so concurrent processes don't read a partial binary.

### Versioned vs unversioned files

When `RIG_BINARY` is set (explicit override), use **unversioned** file names:
- `{rigDir}/rigd.addr`
- `{rigDir}/rigd.lock`

When using a managed binary (any other resolution path), use **versioned** file names:
- `{rigDir}/rigd-v{version}.addr`
- `{rigDir}/rigd-v{version}.lock`

This allows multiple SDK versions to run their own `rigd` instances simultaneously without conflicting.

### Server startup protocol

1. **Fast path**: read the addr file. If it exists and `GET /health` returns `200`, the server is already running. Return `http://{addr}`.

2. **Acquire lock**: create the lock file and acquire an exclusive `flock`. This prevents concurrent test processes from starting multiple servers.

3. **Double-check**: after acquiring the lock, re-read the addr file and probe health. Another process may have started the server while we waited for the lock.

4. **Start server**: launch `rigd` as a detached process (new session via `setsid`):
   ```
   rigd --idle 5m --rig-dir {rigDir} [--addr-file {addrFile}]
   ```
   Pass `--addr-file` only when using versioned file names. Redirect stderr to `{rigDir}/rigd.log` (append mode).

5. **Wait for addr file**: poll every 100ms for up to 10 seconds. Once the file appears and contains a non-empty address, probe `GET /health`. Return `http://{addr}` on success.

6. **Release lock**: unlock and remove the lock file.

The `--idle 5m` flag makes `rigd` exit after 5 minutes of inactivity. Multiple test processes share the same server instance; the idle timer resets on each API call.

---

## Go SDK Defaults

Behaviors the Go SDK applies that other SDKs should replicate:

| Behavior | Go SDK default | Protocol default |
|----------|---------------|-----------------|
| Traffic proxying (`observe`) | `true` | `false` |
| Startup timeout | 2 minutes | No timeout |
| Server auto-start | Yes, via `EnsureServer()` | N/A |
| Cleanup on teardown | `DELETE` with `?log=true` | N/A |

### Server auto-start

The Go SDK auto-starts `rigd` if no server is running:

1. Check `RIG_SERVER_ADDR` env var — if set, use that server directly
2. Otherwise, follow the [server startup protocol](#server-startup-protocol) described above

### SSE stream handling

The Go SDK:

1. Connects to `GET /environments/{id}/events`
2. For each event:
   - `callback.request` → dispatch to registered handler, post response
   - `environment.up` → extract `ingresses` map, return `Environment` to caller
   - `environment.down` → return error with `message` as the error text
   - `environment.failing` → cache error for timeout message
   - `progress.stall` → cache `message` for timeout display
3. Blocks until `environment.up` or `environment.down`
4. On startup timeout: fail with the most recent `progress.stall` message if available

### Cleanup

On test cleanup:

1. Cancel any running client-side functions
2. `DELETE /environments/{id}?log=true&reason={outcome}`
   - `reason=test_failed` if the test failed
   - `reason=test_passed` if the test passed
3. Block until DELETE response
4. Log event log file paths for debugging
