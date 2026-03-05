# SDK Reference

Authoritative reference for how rig client SDKs should behave. The Go SDK (`github.com/matgreaves/rig/client`) is the reference implementation; examples use Go but the behaviors apply to all languages.

For the wire protocol, see [protocol.md](protocol.md).

## Defaults

SDKs should apply these defaults:

| Behavior | Default | Notes |
|----------|---------|-------|
| Traffic proxying (`observe`) | `true` | Protocol default is `false`; SDKs opt in |
| Startup timeout | 2 minutes | Fail with `progress.stall` message if available |
| Server auto-start | Yes | Follow the [server startup protocol](protocol.md#server-startup-protocol) |
| Cleanup on teardown | `DELETE` with `?log=true` | Include `reason=test_failed` on failure; omit `reason` on pass |
| Preserve temp dir | No | Controlled via `RIG_PRESERVE` / `RIG_PRESERVE_ON_FAILURE` env vars |
| Host env capture | Yes | Capture `os.Environ()` and send as `host_env` in the spec |
| Working dir capture | Yes | Capture `os.Getwd()` and send as `dir` in the spec |

---

## SSE Stream Handling

SDKs connect to `GET /environments/{id}/events` and process events in a loop:

| Event | Behavior |
|-------|----------|
| `callback.request` (type `"hook"`) | Dispatch to registered handler synchronously, post `callback.response` |
| `callback.request` (type `"start"`) | Launch handler asynchronously, post `callback.response` immediately |
| `callback.request` (type `"publish"`) | Respond with endpoint data after publishing |
| `callback.request` (type `"ready"`) | Respond after service is ready |
| `environment.up` | Extract `ingresses` map, return resolved environment to caller |
| `environment.down` | Return error with `message` as the error text |
| `progress.stall` | Cache `message` -- use as the error text if startup times out |

The stream blocks until `environment.up` or `environment.down`. On startup timeout, fail with the most recent `progress.stall` message if available.

---

## Cleanup Flow

When the test finishes:

1. Cancel all client-side function contexts (stops `Func`/`client` services)
2. `DELETE /environments/{id}?log=true[&reason=test_failed]`
   - Include `reason=test_failed` if the test failed
   - Omit `reason` on success
3. Block until DELETE response
4. Log event log file paths for debugging

### Preserve env vars

| Variable | Effect |
|----------|--------|
| `RIG_PRESERVE=true` | Keep environment temp directory after every test |
| `RIG_PRESERVE_ON_FAILURE=true` | Keep temp directory only when the test fails |

---

## Test Assertions (`test.note`)

SDKs should forward test assertion failures to `rigd` as `test.note` events via `POST /environments/{id}/events`. This places assertions in the event log alongside service lifecycle and traffic events, giving a unified timeline.

```go
// Go SDK wraps testing.TB automatically:
env.T.Errorf("expected 200 but got %d", resp.StatusCode)
// ^ also posts test.note to rigd event log
```

---

## Log Writer for Client-Side Services

Client-side services (`Func` / `client` type) need a mechanism to ship stdout/stderr to `rigd` as `service.log` events. SDKs should:

- Buffer partial writes until a newline
- Batch burst lines into a single HTTP POST (newline-joined `log_data`)
- Never block the caller on HTTP I/O (use a background sender with a bounded queue)
- Flush remaining buffered data when the function context is cancelled

The Go SDK uses a 256-element channel with drop-on-full semantics.

---

## Service Builders

SDK-specific sugar for constructing the JSON spec. The table below shows the defaults each builder should apply when converting to the wire format.

### Go module (`"go"`)

Builds and runs a Go module as a subprocess.

- **Default ingress**: `"default"`, HTTP
- **Config**: `{"module": "..."}`

```go
rig.Go("./cmd/api").
    Egress("db").
    Args("--verbose")
```

### In-process function (`"client"`)

Runs a function in the test process as a service.

- **Default ingress**: `"default"`, HTTP
- **Lifecycle**: server allocates ports and runs health checks; start is delegated via `"start"` callback

```go
rig.Func(func(ctx context.Context) error {
    w, _ := connect.ParseWiring(ctx)
    return http.ListenAndServe(w.Ingress().HostPort, handler)
})
```

### Process (`"process"`)

Runs a pre-built binary as a subprocess.

- **Default ingress**: `"default"`, HTTP
- **Config**: `{"command": "...", "dir": "..."}`

```go
rig.Process("/usr/local/bin/myservice").
    Dir("/opt/app").
    Args("--port=${PORT}")
```

### Container (`"container"`)

Runs a Docker container with host-mapped ports.

- **Default ingress**: `"default"`, HTTP (must set container port)
- **Config**: `{"image": "...", "cmd": [...], "env": {...}}`

```go
rig.Container("redis:7").
    Port(6379).
    Ingress("default", rig.IngressTCP())
```

### Postgres (`"postgres"`)

Runs a PostgreSQL container with automatic wiring.

- **No user-defined ingress**: fixed TCP on port 5432
- **Default image**: `postgres:16-alpine`
- **Published attributes**: `PGHOST` (`${HOST}`), `PGPORT` (`${PORT}`), `PGDATABASE` (= service name), `PGUSER`, `PGPASSWORD`

Address-derived attributes use template variables (`${HOST}`, `${PORT}`) and are resolved automatically when the endpoint is consumed. This means they stay correct through container port remapping and proxy address rewriting.

```go
rig.Postgres().
    InitSQL("CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT)")
```

### Redis (`"redis"`)

Managed Redis container with automatic database isolation.

- **No user-defined ingress**: fixed TCP on port 6379
- **Default image**: `redis:7-alpine`
- **Published attributes**: `REDIS_URL` (`redis://${HOST}:${PORT}/{db}`)
- **Pooled**: shares a single container across test environments; each gets an isolated database number

`REDIS_URL` uses template variables (`${HOST}`, `${PORT}`) so it stays correct through proxy address rewriting. The database number is assigned automatically by the pool.

```go
rig.Redis()
rig.Redis().Image("redis:6-alpine")
```

### S3 (`"s3"`)

Managed S3-compatible object storage backed by MinIO.

- **No user-defined ingress**: fixed TCP on port 9000
- **Published attributes**: `S3_ENDPOINT` (`http://${HOST}:${PORT}`), `S3_BUCKET` (bucket name), `AWS_ACCESS_KEY_ID` (`rigadmin`), `AWS_SECRET_ACCESS_KEY` (`rigadmin`)
- **Pooled**: shares a single MinIO container across test environments; each gets an isolated bucket

`S3_ENDPOINT` uses template variables (`${HOST}`, `${PORT}`) so it stays correct through proxy address rewriting. The bucket name is assigned automatically by the pool.

```go
rig.S3()
```

### SQS (`"sqs"`)

Managed SQS-compatible message queue backed by ElasticMQ.

- **No user-defined ingress**: fixed TCP on port 9324
- **Published attributes**: `SQS_ENDPOINT` (`http://${HOST}:${PORT}`), `SQS_QUEUE_URL` (queue URL), `AWS_ACCESS_KEY_ID` (`rig`), `AWS_SECRET_ACCESS_KEY` (`rig`)
- **Pooled**: shares a single ElasticMQ container across test environments; each gets an isolated queue

`SQS_ENDPOINT` uses template variables (`${HOST}`, `${PORT}`) so it stays correct through proxy address rewriting. The queue URL is assigned automatically by the pool.

```go
rig.SQS()
```

### Kafka (`"kafka"`)

Runs a Redpanda container with Kafka and schema registry.

- **Default ingresses**: `"default"` (Kafka on port 9092) + `"schema-registry"` (HTTP on port 8081)
- **Default image**: `redpandadata/redpanda:v24.3.1`
- **No published attributes**: use `ep.HostPort` as bootstrap servers directly
- **Not pooled**: each test gets a fresh container (avoids topic name collisions)

Access endpoints from tests:

```go
ep := env.Endpoint("kafka")                        // bootstrap servers = ep.HostPort
sr := env.Endpoint("kafka", "schema-registry")     // schema registry = http://{sr.HostPort}
```

Services that depend on Kafka wire both ingresses as separate egresses:

```go
rig.Go("./cmd/worker").
    Egress("kafka").                                        // → default ingress
    EgressAs("schema-registry", "kafka", "schema-registry") // → schema-registry ingress
```

Schema registration via `AvroSchema`/`ProtoSchema` reads the file at call time and POSTs it to the schema registry during init. The subject name is derived from the filename (sans extension).

```go
rig.Kafka()
rig.Kafka().Image("redpandadata/redpanda:v24.1.1")
rig.Kafka().AvroSchema("schemas/user-value.avsc")   // registers subject "user-value"
rig.Kafka().ProtoSchema("schemas/order-key.proto")   // registers subject "order-key"
```

### Temporal (`"temporal"`)

Downloads and runs a Temporal dev server.

- **Default ingresses**: `"default"` (gRPC) + `"ui"` (HTTP)
- **Default CLI version**: `1.5.1`
- **Published attributes**: `TEMPORAL_ADDRESS` (`${HOSTPORT}`), `TEMPORAL_NAMESPACE`
- **Pooled**: shares a single dev server process across test environments; each gets an isolated namespace

`TEMPORAL_ADDRESS` uses the `${HOSTPORT}` template variable, which resolves to `host:port` — staying correct through proxy address rewriting. The namespace is assigned automatically by the pool.

```go
rig.Temporal()
rig.Temporal().Version("1.5.1")
```

### Custom

Extensible builder for server-registered types not yet modeled in the SDK.

- **Default ingress**: `"default"`, HTTP

```go
rig.Custom("redis", map[string]any{"image": "redis:7-alpine"})
```

---

## Builder Default Summary

| Builder | Default ingress | Protocol | Notes |
|---------|----------------|----------|-------|
| Go module | `"default"` | HTTP | |
| Function | `"default"` | HTTP | |
| Process | `"default"` | HTTP | |
| Container | `"default"` | HTTP | Must set container port |
| Postgres | (automatic) | TCP | Fixed port 5432, no user override |
| Redis | (automatic) | TCP | Fixed port 6379, no user override |
| S3 | (automatic) | TCP | Fixed port 8333, no user override |
| SQS | (automatic) | TCP | Fixed port 9324, no user override |
| Kafka | `"default"` + `"schema-registry"` | Kafka + HTTP | Ports 9092 + 8081, not pooled |
| Temporal | `"default"` + `"ui"` | gRPC + HTTP | |
| Custom | `"default"` | HTTP | |

### Ingress constructors (Go SDK)

```go
rig.IngressHTTP()  // IngressDef{Protocol: rig.HTTP}
rig.IngressTCP()   // IngressDef{Protocol: rig.TCP}
rig.IngressGRPC()  // IngressDef{Protocol: rig.GRPC}
rig.IngressKafka() // IngressDef{Protocol: connect.Kafka}
```

### Health check override

```go
svc.Ingress("default", rig.IngressDef{
    Protocol: rig.HTTP,
    Ready: &rig.ReadyDef{
        Path:     "/healthz",
        Timeout:  60 * time.Second,
        Interval: 500 * time.Millisecond,
    },
})
```

Server defaults (when not overridden): initial interval `10ms` with exponential backoff to `1s`, timeout `30s`.
