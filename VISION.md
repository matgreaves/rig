# rig — Future Vision: Unified Test Observability

> This document captures the long-term vision for rig. The transparent proxy layer is now implemented — everything from "Transparent Proxy Layer" through "Opting In" below describes shipped functionality. The dashboard, session recording, and chaos injection sections remain future work that builds on the proxy and event log foundations already in place.

## The Insight

rig has a property that no other local development tool has: **it controls every edge in the service graph at the application level.**

Services don't discover their dependencies via DNS, environment convention, or configuration files they manage themselves. They're *told* where their dependencies are via env vars and config that rig injects. A service asking "where is my database?" gets an address from rig. If rig gives it a proxy address instead of the real address, the service connects to the proxy without knowing or caring.

This means rig can transparently insert infrastructure between any two services — proxies, loggers, fault injectors — without modifying any service code, without sidecars, without a service mesh, without Kubernetes.

Docker Compose can't do this because services find each other by DNS name. Kubernetes service meshes can do this but require privileged iptables manipulation and a control plane. rig does it by changing a number in an env var.

---

## Transparent Proxy Layer — SHIPPED

For each egress→ingress edge where rig knows the protocol, it inserts a reverse proxy:

```
Without proxy:
  order-service → 127.0.0.1:54321 (postgres)

With proxy:
  order-service → 127.0.0.1:61234 (rig proxy) → 127.0.0.1:54321 (postgres)
```

The only thing that changes is the address in the wiring. The service's code is identical.

### Protocol-Aware Proxying — SHIPPED

Each ingress declares its protocol in the spec. rig proxies each type:

| Protocol | Proxy | Captures |
|----------|-------|----------|
| HTTP | `httputil.ReverseProxy` | Method, path, headers, status code, latency, body size |
| gRPC | HTTP/2 reverse proxy | Service/method, metadata, status code, latency. Decode payloads if reflection available |
| TCP | Raw byte-level relay | Connection open/close, bytes transferred, timing. Cannot decode payloads |

### Request/Response Events — SHIPPED

Each proxied request becomes an event on the event bus:

```go
const (
    EventRequestStarted   = "request.started"
    EventRequestCompleted = "request.completed"
    EventRequestFailed    = "request.failed"
)

// Additional fields on Event for request tracking:
type RequestInfo struct {
    RequestID    string        // unique per request
    Source       string        // "order-service" (the service making the request)
    Target       string        // "postgres" (the logical service name, not the proxy)
    Egress       string        // "database" (the egress name on the source)
    Method       string        // "POST" / "OrderService.CreateOrder"
    Path         string        // "/orders"
    StatusCode   int           // 201
    Latency      time.Duration // 2ms
    RequestSize  int64         // bytes
    ResponseSize int64         // bytes
}
```

These events flow through the same event log, the same SSE stream, and the same session recordings as every other event. No new infrastructure.

### On by Default — SHIPPED

Proxy insertion is **on by default** — every environment gets full traffic observability with zero configuration. Proxying adds ~1ms of latency per request, which is negligible for most testing but could affect latency-sensitive benchmarks. Disable with `WithoutObserve()` in the Go SDK or `"observe": false` in the spec.

---

## Live Dashboard — FUTURE

### CLI Dashboard (TUI)

`rig up spec.json` starts all services and renders a terminal dashboard:

```
┌─ rig ─ order-workflow ──────────────────────────────────────────────────┐
│                                                                          │
│  Services                                                                │
│  ● postgres      ready   127.0.0.1:54321   (2.1s startup)              │
│  ● temporal      ready   127.0.0.1:7233    (3.4s startup)              │
│  ● order-service ready   127.0.0.1:8080    (1.2s startup)              │
│                                                                          │
│  Traffic ─────────────────────────────────────────────────────────────── │
│  12:01:03.412  order-service → postgres    POST /orders          201 2ms│
│  12:01:03.415  order-service → temporal    StartWorkflow          OK 8ms│
│  12:01:03.891  temporal      → order-svc   POST /webhook/complete 200 1ms│
│  12:01:04.102  order-service → postgres    SELECT orders         200 1ms│
│                                                                          │
│  Logs (order-service) ────────────────────────────────────────────────── │
│  12:01:03.410  starting HTTP server on :8080                             │
│  12:01:03.411  connected to postgres                                     │
│  12:01:03.412  POST /orders — created order ord_abc123                   │
│                                                                          │
│  [s]ervices  [t]raffic  [l]ogs  [g]raph  [q]uit                         │
└──────────────────────────────────────────────────────────────────────────┘
```

The dashboard is just another SSE consumer. It reads the same event stream that the test SDK reads. Same events, different rendering.

### Views

- **Services** — status of each service, endpoint addresses, startup duration
- **Traffic** — real-time feed of inter-service requests with method, path, status, latency
- **Logs** — per-service log output, filterable, with service name prefix and colour coding
- **Graph** — live topology diagram with edges lighting up as traffic flows. Edge thickness or colour indicates request rate. Failed requests shown in red

### Web Dashboard (Future)

A browser-based dashboard served by rigd itself. Same data, richer rendering:
- Clickable request details (full headers, body preview)
- Latency distribution histograms per edge
- Service dependency graph with animated traffic flow
- Timeline view of the full environment lifecycle

---

## Session Recording & Replay — NEXT

### Recording

The event log is already a complete, ordered, timestamped record of everything that happened. Recording is just serializing it to disk.

Format: JSONL (one JSON event per line). Compact, streamable, grep-friendly.

```jsonl
{"seq":1,"type":"artifact.started","service":"postgres","artifact":"docker:postgres:16","ts":"2024-01-15T10:30:00.001Z"}
{"seq":2,"type":"artifact.started","service":"order-service","artifact":"go:./cmd/order-service","ts":"2024-01-15T10:30:00.002Z"}
{"seq":3,"type":"artifact.cached","service":"postgres","artifact":"docker:postgres:16","ts":"2024-01-15T10:30:00.050Z"}
{"seq":4,"type":"artifact.completed","service":"order-service","artifact":"go:./cmd/order-service","ts":"2024-01-15T10:30:02.341Z"}
{"seq":5,"type":"service.starting","service":"postgres","ts":"2024-01-15T10:30:02.350Z"}
{"seq":6,"type":"service.starting","service":"temporal","ts":"2024-01-15T10:30:02.351Z"}
{"seq":7,"type":"service.ready","service":"postgres","ts":"2024-01-15T10:30:03.102Z"}
{"seq":8,"type":"request.completed","source":"order-service","target":"postgres","method":"POST","path":"/orders","status":201,"latency_ms":2,"ts":"2024-01-15T10:30:05.412Z"}
{"seq":9,"type":"service.failed","service":"order-service","error":"exit code 1","ts":"2024-01-15T10:30:06.100Z"}
{"seq":10,"type":"environment.down","ts":"2024-01-15T10:30:06.500Z"}
```

### Replay

`rig replay session.jsonl` opens the dashboard in replay mode. The data source changes — file instead of live SSE — but the rendering is identical.

At any sequence number, the full environment state is derivable by replaying events 0 through N:

| State | Derived from |
|-------|-------------|
| Which services exist and their status | `service.starting`, `service.ready`, `service.failed`, `service.stopped` |
| Service log output | `service.log` events up to that point |
| Inter-service requests | `request.completed` events |
| Dependency graph and active edges | `wiring.resolved` + request events |
| Artifact resolution progress | `artifact.*` events |

This is event sourcing. The event log is the single source of truth. The dashboard is a projection.

### Replay Controls

- **Play/pause** at real speed, 2x, 5x, 10x
- **Scrub** — drag a timeline slider to jump to any point
- **Step** — advance one event at a time
- **Jump to failure** — skip to the first `*.failed` event
- **Filter** — show only events for a specific service

### CI Integration

The most impactful use case for replay is debugging CI failures:

1. CI runs tests, one fails
2. The test's session log is saved as a CI artifact (small JSONL file, typically < 1MB)
3. Developer downloads it: `rig replay ci-session-run-4821.jsonl`
4. They watch the environment boot, see services come up, see the exact request that failed, see the error in the service logs — all correlated by timestamp
5. They diagnose the root cause from the first failure, without reproducing it locally

For flaky tests, developers can compare recordings from passing and failing runs side by side.

Session recordings also make flaky test triage a team activity. Someone posts the JSONL file in a thread and multiple people can replay it independently, each examining different aspects.

### Parallel Test Isolation

Each `rig.Up()` call produces its own environment with its own event stream and its own session log. Five parallel tests mean five separate recordings. No interleaving, no correlating timestamps across log streams, no guessing which output belongs to which test.

---

## Chaos & Fault Injection — FUTURE

Because the proxy sits between every service, it can do more than observe:

- **Latency injection** — add artificial delay to specific edges to test timeout handling
- **Error injection** — return 500s or connection resets on a percentage of requests to specific services
- **Partition simulation** — temporarily block traffic between two services
- **Bandwidth limiting** — throttle an edge to simulate slow networks

These would be controlled via the spec or the dashboard at runtime. The proxy infrastructure is the same; only the proxy behaviour changes.

```json
{
  "egresses": {
    "database": {
      "service": "postgres",
      "chaos": {
        "latency": "200ms",
        "error_rate": 0.1
      }
    }
  }
}
```

This is speculative and far-future, but it falls naturally out of the proxy architecture.

---

## The Chrome DevTools Analogy

Before browser DevTools, frontend debugging was `console.log`, refresh, read output. DevTools gave developers the Network tab — every request, every response, timing waterfall — without changing application code. It was a step change in frontend development productivity.

rig's dashboard vision is the Network tab for microservice development:

- **No instrumentation required** — no OpenTelemetry SDKs, no tracing libraries, no code changes
- **No infrastructure required** — no Kubernetes, no Istio, no Jaeger, no Grafana
- **Zero config** — write egresses in your spec, get full visibility
- **Works locally** — not a production observability tool, a development tool

The reason no existing tool provides this for local development is that no other tool controls the wiring. Docker Compose uses DNS. Kubernetes uses kube-proxy and iptables. rig controls application configuration directly, which is the simplest and most reliable interception point.

---

## What This Means for Development Teams

### Debugging Cross-Service Issues

**Today**: Open N terminal windows, tail N log streams, reproduce the request, correlate timestamps across log outputs, add more logging, rebuild, reproduce again. 30–60 minutes of log archaeology.

**With rig**: One dashboard, one traffic view. See the exact request that failed, the error response, and the relevant service logs at that timestamp. 2 minutes.

### Onboarding

**Today**: Read a README, install tools, configure env vars, start services in the right order, hope it works.

**With rig**: `rig up`. The graph view shows the architecture. Traffic view shows how services interact. The spec is the documentation, and it's always accurate because it's the thing that actually runs.

### CI Failure Debugging

**Today**: Read raw CI logs, try to reproduce locally, add more logging, push, wait for CI, repeat.

**With rig**: Download the session recording, replay the exact failure, diagnose from the first occurrence.

---

## Architectural Foundations — SHIPPED

Everything in this document builds on foundations that are built and working:

1. **Event log as single source of truth** — coordination, observability, and proxy traffic all flow through the same log
2. **SSE streaming to clients** — the SDK consumes lifecycle events, callback requests, and proxy traffic on a single stream
3. **Explicit egress wiring** — proxy insertion is a wiring change, not a new mechanism. Implemented in `server/lifecycle.go`
4. **Protocol declarations on ingresses** — the proxy dispatches to HTTP, TCP, or gRPC handlers based on protocol. Implemented in `server/proxy/`
5. **Session-scoped event streams** — parallel tests produce isolated recordings with no interleaving

The remaining vision items (dashboard, recording, replay, chaos) are consumers of the event stream and proxy infrastructure that already exist. No engine changes are needed.