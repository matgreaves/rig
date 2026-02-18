# rig — Comparative Analysis

## Overview

rig occupies a space between test-scoped container libraries (testcontainers, dockertest) and full development environment orchestrators (docker compose, tilt, dagger). It borrows ideas from several of these but takes a distinct architectural approach: a standalone server with thin multi-language SDKs connected via HTTP/JSON + SSE, explicit service graphs with automatic wiring, and pluggable service backends that go beyond containers.

Beyond test orchestration, rig's architecture — an event-sourced log, explicit egress wiring, and SSE streaming — creates a foundation for unified test observability: transparent inter-service proxying, live dashboards, session recording, and replay. No other tool in this space has this combination of properties. See [VISION.md](./VISION.md) for the full vision.

---

## Testcontainers

[testcontainers.com](https://testcontainers.com/) — Available in Go, Java, .NET, Python, Node, Rust, Haskell.

**What it does well:**
- Mature ecosystem with modules for dozens of services (databases, message brokers, cloud emulators)
- Deep language integration — each SDK feels native to its language and test framework
- GenericContainer API allows arbitrary Docker images
- Network and volume management within tests
- Reusable containers across test runs (`testcontainers.Reuse`)
- Cloud-based parallel execution via Testcontainers Cloud
- Large community and extensive documentation

**Where rig differs:**

| Dimension | Testcontainers | rig |
|-----------|---------------|------|
| **Architecture** | Thick library per language. Each SDK reimplements container management, health checks, and resource cleanup | Thin SDK per language (HTTP client + SSE reader). Server does all the work. Adding a new language SDK is small — no server to implement, no Docker client needed |
| **Service backends** | Containers only. Everything must be a Docker image | Containers, processes, Go modules, scripts, binaries, client-side functions. Use whatever makes sense |
| **Service wiring** | Manual. Each container is started independently. The test author is responsible for passing connection details between services | Automatic. Declare egresses → ingresses in the spec. rigd allocates ports, resolves addresses, and injects configuration. Services never know each other's ports |
| **Parallel safety** | Possible but requires care. Random ports help, but wiring between containers is still manual per instance | First-class. The same spec runs N times simultaneously with zero coordination from the test author. All ports are dynamically allocated and wired automatically |
| **Service graph** | Implicit. Start order is whatever the test code does. No dependency model | Explicit DAG. Ingress/egress relationships define dependencies. Startup ordering emerges from the event bus. Cyclic dependencies are caught at validation time with clear error messages |
| **Lifecycle hooks** | `WithStartupCommand`, `WithAfterReadyCommand` on individual containers. Limited to shell commands inside the container | Rich hook system: prestart (full wiring, write config files) and init (seed data, create resources). Hooks can be builtins, scripts, or client-side closures that capture test state |
| **Client-side functions** | Not supported. All logic runs inside containers or in test code after container startup | Core feature. Inline closures run on the client side at specific lifecycle points. Close over test-local state for assertions. The server sends callback requests via SSE; the client executes and POSTs results back |
| **Artifact caching** | Docker layer caching only. No concept of compiling source code or downloading binaries | Dedicated artifact phase with content-addressable global cache. Go module compilation, binary downloads, and Docker pre-pulls are deduplicated across environments and test runs |
| **Observability** | Per-container log consumers. No unified event model | Central event log with structured events. All lifecycle events (publish, start, ready, init, logs) flow through one observable, replayable log. SSE streams to clients. Foundation for transparent proxy layer and session replay (see [VISION.md](./VISION.md)) |
| **Configuration injection** | Environment variables set per container at creation time. No convention for cross-service config | Attribute system with well-known env var names (PGHOST, TEMPORAL_ADDRESS, etc.). Template expansion in args. Automatic prefix rules for multi-egress services |

**Where testcontainers wins:**
- Maturity. Years of production use, battle-tested edge cases, huge module ecosystem
- No separate server process to manage — it's just a library
- Testcontainers Cloud for remote execution
- Container-specific features: exec into containers, copy files in/out, custom wait strategies with log parsing

**Where rig wins:**
- Non-container services. Running a Go binary you just compiled, a Temporal dev server from a binary, or a client-side seed function doesn't require packaging everything as a Docker image
- Automatic wiring eliminates an entire class of boilerplate and port-conflict bugs
- The artifact phase means tests don't block on Docker pulls or compilation during the service graph execution
- Client-side closures are a fundamentally different capability — you can't close over a `*sql.DB` in testcontainers
- The event log enables session recording and replay for CI failure debugging — no equivalent exists in testcontainers

---

## Docker Compose

[docs.docker.com/compose](https://docs.docker.com/compose/) — YAML-based multi-container orchestration.

**What it does well:**
- Ubiquitous. Every developer knows it
- Declarative YAML with service dependencies (`depends_on`), networks, volumes
- `docker compose up` / `down` is simple
- Health checks with `healthcheck` directive
- Variable interpolation with `.env` files and `${VAR}` syntax
- Profiles for optional services

**Where rig differs:**

| Dimension | Docker Compose | rig |
|-----------|---------------|------|
| **Scope** | Development/CI environments. Not designed for test framework integration | Purpose-built for test suites. Integrates with test lifecycle (setup/teardown, assertions, closures) |
| **Port allocation** | Static in the YAML or random via `ports: - "5432"`. No cross-service wiring of allocated ports | Fully dynamic. All ports allocated at runtime. Wiring is automatic — services receive their dependencies' addresses without configuration |
| **Parallel instances** | Running multiple copies requires different project names, port ranges, network names. Fragile | First-class. Same spec, unlimited instances, zero conflicts |
| **Service backends** | Containers only (with `build:` for local Dockerfiles) | Containers, processes, Go modules, scripts, binaries, client-side functions |
| **Programmability** | YAML is static. No logic, no closures, no computed values | Specs are built programmatically from native language types. Inline functions close over test state |
| **Lifecycle hooks** | Limited to `healthcheck` and `entrypoint`/`command` overrides | Prestart and init hooks with full wiring context. Builtins for common tasks (DB migrations, Temporal setup) |
| **Language integration** | None. Tests shell out to `docker compose up` and parse output for ports | Native SDK per language. `rig.Up(t, ...)` returns typed endpoints. Cleanup via `t.Cleanup` |
| **Observability** | `docker compose logs`. Per-service, not unified. No inter-service traffic visibility | Central event log with structured events. SSE streaming. Foundation for transparent proxy layer that captures inter-service requests without any service instrumentation (see [VISION.md](./VISION.md)) |
| **Inter-service visibility** | None. Compose doesn't know which services talk to each other or what they send | Explicit egress model means rig knows every edge in the service graph. Combined with transparent proxy insertion (possible because rig controls the wiring), every inter-service request can be captured and displayed — the "Network tab for microservices" |

**Where Docker Compose wins:**
- Zero learning curve. Every team already uses it
- Standalone — no separate server process
- Good for long-lived development environments (not just tests)
- Docker Compose Watch for live reloading
- Broad ecosystem of example compose files

**Where rig wins:**
- Test integration. Compose doesn't know about your test framework, assertions, or lifecycle
- Dynamic wiring eliminates hardcoded ports and fragile `.env` files
- Client-side functions can't be replicated in YAML
- Non-container services avoid the "Dockerize everything for tests" tax
- The event log and explicit service graph enable observability that Compose structurally cannot provide — Compose uses DNS-based service discovery, so it can't intercept traffic without iptables or a sidecar per container

---

## Dagger

[dagger.io](https://dagger.io/) — Programmable CI/CD engine using containers.

**What it does well:**
- SDKs in Go, Python, TypeScript — pipelines as code
- Hermetic execution via containerised steps
- Content-addressable caching of every operation
- GraphQL API between the engine and SDKs
- Composable modules that can be shared across projects

**Where rig differs:**

| Dimension | Dagger | rig |
|-----------|--------|------|
| **Purpose** | CI/CD pipelines. Build, test, deploy as code | Test environment orchestration. Start services, wire them, run tests against them |
| **Execution model** | Every step runs in a container. The pipeline IS a container DAG | Services run however makes sense (container, process, binary). The DAG is about service dependencies, not build steps |
| **Lifecycle** | Steps are short-lived. Run a command, produce an output, pass it to the next step | Services are long-lived. Start, wait for ready, keep running while tests execute, tear down |
| **Wiring** | Outputs of one step are inputs to the next (files, directories, env vars) | Ingress/egress graph with resolved endpoints. Services discover each other via well-known env vars |
| **Test integration** | Dagger can run tests as a pipeline step, but doesn't integrate with test frameworks | Purpose-built for test frameworks. `t.Cleanup`, inline assertions, closure capture |
| **Client-side execution** | Everything runs in containers on the Dagger engine | Client-side functions run in the test process. Close over local state |
| **Engine management** | SDK auto-manages the Dagger engine (download, start, connect via OCI image) | Same pattern, simplified — single binary per platform, hash-based versioning, file-lock coordination |

**Where Dagger wins:**
- Hermetic, reproducible execution. Every step is containerised
- Powerful caching that goes far beyond what rig's artifact cache does
- CI/CD is a broader use case — build, test, AND deploy
- Module ecosystem for sharing pipeline components

**Where rig wins:**
- Long-lived service orchestration. Dagger's step model doesn't naturally express "start postgres and keep it running while I run 50 test cases"
- Client-side closures. Dagger can't run a function in your test process that closes over local variables
- Lighter weight. No need to containerise every step. Run a Go binary directly
- The event log and SSE streaming provide real-time observability of what's happening during service startup — Dagger's model is more batch-oriented

---

## Tilt

[tilt.dev](https://tilt.dev/) — Development environment orchestrator (now part of Docker).

**What it does well:**
- Kubernetes-native with support for docker-compose and local processes
- Live update / hot reload for fast development loops
- Tiltfile (Starlark) for programmable environment definitions
- Web UI dashboard for monitoring services
- Resource dependencies and ordering
- Extensions ecosystem

**Where rig differs:**

| Dimension | Tilt | rig |
|-----------|------|------|
| **Purpose** | Development environments with live reload | Test environments with lifecycle hooks and assertions |
| **Primary target** | Kubernetes clusters (with docker-compose and local fallback) | Local services: containers, processes, binaries. No Kubernetes assumption |
| **Configuration** | Starlark (Python-like) Tiltfile | JSON spec from native language types. Go closures, not a scripting language |
| **Service wiring** | Manual. Tilt manages resources but doesn't wire ports between them | Automatic ingress/egress wiring with dynamic port allocation |
| **Test integration** | Tilt can run tests but isn't a test framework tool. It's a development loop tool | Purpose-built for test suites. Setup, assertions, teardown, closures |
| **Lifecycle** | Continuous. Services stay up, rebuild on file change, hot reload | Ephemeral. Environment starts, tests run, environment tears down |
| **Observability** | Built-in web dashboard showing resource status and logs | Event log + SSE stream. Foundation for dashboard, session recording, and transparent proxy layer. The dashboard is a consumer of the event log, not a separate system |
| **Inter-service traffic** | No visibility. Tilt manages resources but doesn't observe their communication | Explicit egress model enables transparent proxy insertion for request-level visibility between services |

**Where Tilt wins:**
- The live-reload development loop. Tilt is unmatched for "change code, see result"
- Kubernetes support for teams whose production is k8s
- Web UI is excellent for observing what's happening
- Starlark is more expressive than JSON for complex environment logic

**Where rig wins:**
- Test-first design. Tilt doesn't know about `*testing.T`, closures, or assertion libraries
- Automatic wiring. Tilt requires manual port forwarding and environment variable setup
- Artifact phase with dedup. Tilt rebuilds on file change; rig caches by content hash
- Client-side functions for test state capture
- Session recording and replay for offline debugging of failures — Tilt's dashboard is live-only

---

## ory/dockertest

[github.com/ory/dockertest](https://github.com/ory/dockertest) — Go library for integration tests with Docker.

**What it does well:**
- Simple Go API for starting containers in tests
- Built-in retry/health check with `pool.Retry`
- Direct Docker API access for fine-grained control
- Lightweight — just a Go library, no separate process

**Where rig differs:**

| Dimension | dockertest | rig |
|-----------|-----------|------|
| **Language support** | Go only | Any language via HTTP/SSE SDK |
| **Service backends** | Containers only | Containers, processes, Go modules, scripts, builtins, client functions |
| **Service wiring** | Manual. Start containers, extract ports, pass to next container | Automatic. Declare the graph, get wired endpoints |
| **Lifecycle hooks** | None. Health check only | Prestart and init hooks with wiring context |
| **Parallel safety** | Random ports per container but no coordination between them | Server-global port allocator, wiring is automatic |
| **Scope** | Single container at a time. Multi-container setups are imperative code | Declarative environment spec with dependency graph |

**Where dockertest wins:**
- Zero ceremony. `pool.RunWithOptions(...)` and you're done
- No server process. It's a library
- Direct Docker API access for advanced use cases

**Where rig wins:**
- Everything beyond "start one container and connect to it"
- Multi-service environments with automatic wiring
- Non-container services
- Multi-language support

---

## Summary Matrix

| Feature | rig | Testcontainers | Docker Compose | Dagger | Tilt | dockertest |
|---------|------|---------------|----------------|--------|------|-----------|
| Multi-language SDK | ✅ thin (HTTP + SSE) | ✅ thick per-lang | ❌ YAML only | ✅ | ❌ Starlark | ❌ Go only |
| Non-container services | ✅ | ❌ | ❌ | ❌ all containers | ✅ local/k8s | ❌ |
| Automatic port wiring | ✅ | ❌ manual | ❌ manual | N/A | ❌ manual | ❌ manual |
| Parallel-safe by default | ✅ | ⚠️ possible | ⚠️ fragile | ✅ | ❌ | ⚠️ possible |
| Client-side closures | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Lifecycle hooks | ✅ prestart + init | ⚠️ limited | ❌ | N/A | ❌ | ❌ |
| Artifact caching | ✅ global dedup | ❌ Docker layers | ❌ Docker layers | ✅ excellent | ⚠️ rebuild | ❌ |
| Event log / observability | ✅ event-sourced | ❌ | ❌ | ⚠️ | ✅ UI | ❌ |
| Session recording & replay | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Inter-service traffic visibility | ✅ via proxy | ❌ | ❌ | ❌ | ❌ | ❌ |
| Service graph model | ✅ ingress/egress DAG | ❌ flat | ⚠️ depends_on | ✅ DAG | ⚠️ deps | ❌ flat |
| Extensible service types | ✅ register custom | ❌ containers only | ❌ containers only | ✅ modules | ✅ extensions | ❌ |
| No separate server | ❌ needs rigd | ✅ library | ✅ CLI | ❌ engine | ❌ daemon | ✅ library |
| Maturity | 🆕 new | ✅ mature | ✅ mature | ✅ growing | ✅ mature | ✅ stable |
| Robust cleanup on crash | ✅ onexit | ⚠️ Ryuk sidecar | ❌ manual | ✅ | ⚠️ | ❌ |

---

## Unique Strengths of rig

**1. Automatic wiring is the killer workflow improvement.**
Every other tool requires the test author to manually extract ports from started services and pass them to dependent services. With N services this is O(N²) boilerplate that's different in every test file. rig reduces this to declaring edges in a graph — the system handles the rest.

**2. Client-side closures bridge the gap between test code and environment setup.**
No other tool lets you run a function in the test process as part of the environment lifecycle. This enables patterns like capturing a `*sql.DB` during init for later assertions, or seeding data with test-specific values computed at runtime. The alternative in every other tool is to do this setup after environment startup in test code, which means you lose the lifecycle ordering guarantees.

**3. The thin SDK model inverts the usual cost of multi-language support.**
Testcontainers maintains separate, thick implementations per language — each with its own Docker client, health check logic, and container management. rig's SDKs are small because the server does the real work. A new language SDK is an HTTP client, an SSE stream reader, and a spec builder. No server to run in the SDK, no Docker client, no port allocator, no health check logic. The correctness and complexity lives in one place.

**4. Non-container services avoid the "Dockerize everything for tests" tax.**
Running a Go service under test shouldn't require a Dockerfile, a registry, and a multi-minute image build. `rig.Go("./cmd/my-service")` compiles the module, caches the binary, and runs it as a process with full wiring. The artifact cache means the second run is instant.

**5. The artifact phase eliminates a class of timing bugs.**
Other tools interleave artifact resolution (pulling images, downloading binaries) with service startup. This means a service might time out waiting for a dependency that's still downloading its Docker image. rig resolves everything upfront in a separate phase — when the service graph starts executing, all artifacts are local.

**6. The event log is the foundation for observability no other test tool provides.**
The event log is event-sourced — every state change is an immutable, sequenced, timestamped event. This is the coordination mechanism (services block on `WaitFor`), the observability mechanism (SSE streams events to clients in real time), and the recording mechanism (serialize to disk for offline replay). No other tool in this space has a unified event log that serves all three purposes. This enables:
- **Session recording and replay** — a JSONL file captures the complete history of an environment's lifetime. CI failures can be debugged by replaying the recording, without reproducing locally.
- **Live dashboards** — a TUI or web UI that consumes the same SSE stream the SDK uses, showing service status, logs, and lifecycle progress.
- **Transparent inter-service proxy** — because rig controls the wiring (services receive dependency addresses from rig, not from DNS), it can insert a proxy on any edge by changing the injected address. The proxy captures requests and publishes them as events on the same bus. Zero instrumentation, zero code changes, zero infrastructure. See [VISION.md](./VISION.md).

**7. Explicit egress wiring enables transparent interception.**
This is the architectural property that no other tool has. Docker Compose uses DNS-based discovery — services resolve `postgres:5432` and connect directly. Kubernetes service meshes intercept at the network level with iptables. rig controls the *configuration* — it tells services where their dependencies are. This is the simplest and most reliable interception point. It requires no privileged access, no network manipulation, and no sidecars. It works because services never knew the real address in the first place.

---

## Risks and Tradeoffs

**1. Separate server process is operational overhead.**
Every other library-based tool (testcontainers, dockertest) "just works" with no additional process. rigd needs to be running, which means either a manual start, a test helper that starts it, or a system service. This is a real friction point for adoption.

*Mitigation:* The SDK auto-manages the server entirely. Each SDK embeds a content hash of the engine binary it targets. On first use, it downloads, caches, and starts `rigd` transparently — the user never knows there's a separate process. Multiple test processes coordinate via file locks to share a single server. The server idles out automatically after a configurable timeout with no active environments.

The hash-based versioning model also means client-only changes (new builder helpers, SDK bug fixes) don't require a server restart — the hash only changes when the engine itself changes. And breaking protocol changes are safe because the SDK and engine are always compatible by construction — they ship as a pair targeting the same hash. Multiple engine versions coexist in `~/.rig/bin/<hash>/rigd`, so different projects pinning different SDK versions never conflict.

**2. New project, no ecosystem.**
Testcontainers has hundreds of modules for specific services. rig starts with a handful of builtins. Users will encounter services that don't have a builtin yet.

*Mitigation:* The `container` type is the generic escape hatch — any Docker image works. The extensible type system means community-contributed types can grow organically. And the most common services (postgres, temporal, redis) ship as builtins from day one.

**3. Complexity budget.**
The event bus, artifact phase, lifecycle hooks, template expansion, attribute prefix rules, and SSE-based callbacks are a lot of moving parts. Each is individually justified, but together they create a system that's harder to debug when something goes wrong.

*Mitigation:* The event log itself is the answer here — every lifecycle step emits observable events. When something goes wrong, the event stream tells you exactly where and why. The same log that coordinates services also diagnoses problems. Good error messages at each lifecycle step are essential.

**4. SSE connection management.**
The SDK maintains an SSE connection to rigd for the duration of environment startup. If the connection drops (network blip, rigd restart), the SDK loses its event stream. This is a different failure mode than a simple request/response API.

*Mitigation:* SSE has built-in reconnection semantics (`Last-Event-ID` header). The event log is persistent and replayable from any sequence number, so a reconnecting client picks up exactly where it left off. The SDK should implement automatic reconnection with the last-seen sequence number. If rigd crashes entirely, the environment is gone anyway — the failure is clear and immediate.

**5. SDK dependency weight.**
The SDK needs to communicate with the server and process SSE events. Heavy dependencies would make adoption harder, especially in languages like TypeScript or Python where the ecosystem prefers lightweight packages.

*Mitigation:* The entire API is HTTP/JSON + SSE — no protobuf, no codegen, no gRPC dependency. The SDK never runs a server; it only makes outbound HTTP requests and reads an SSE stream. Every language has good libraries for both. An SDK is an HTTP client, an SSE reader, an event loop, and a spec builder. No schema compilation step, no generated code, no heavy runtime dependencies.