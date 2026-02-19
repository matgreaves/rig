package rig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matgreaves/rig/connect"
)

// Re-export shared types from connect/ so users of the SDK never need to
// import connect/ directly.
type (
	Endpoint = connect.Endpoint
	Protocol = connect.Protocol
	Wiring   = connect.Wiring
)

const (
	TCP  = connect.TCP
	HTTP = connect.HTTP
	GRPC = connect.GRPC
)

// Services maps service names to their definitions.
type Services map[string]ServiceDef

// ServiceDef is the interface implemented by all service type builders
// (*GoDef, *ProcessDef, *CustomDef). It is sealed — only types in this
// package implement it.
type ServiceDef interface {
	rigService()
}

// IngressDef defines an endpoint a service exposes. Use the IngressHTTP,
// IngressTCP, or IngressGRPC constructors for the common case. For full
// control (health check overrides, attributes, container ports), use a
// struct literal:
//
//	rig.IngressDef{
//	    Protocol:   rig.HTTP,
//	    Ready:      &rig.ReadyDef{Path: "/healthz"},
//	    Attributes: map[string]any{"KEY": "value"},
//	}
type IngressDef struct {
	Protocol      Protocol
	ContainerPort int            // for container types only
	Ready         *ReadyDef      // optional health check override
	Attributes    map[string]any // static attributes published with this ingress
}

// IngressHTTP returns an IngressDef for an HTTP endpoint.
func IngressHTTP() IngressDef { return IngressDef{Protocol: HTTP} }

// IngressTCP returns an IngressDef for a TCP endpoint.
func IngressTCP() IngressDef { return IngressDef{Protocol: TCP} }

// IngressGRPC returns an IngressDef for a gRPC endpoint.
func IngressGRPC() IngressDef { return IngressDef{Protocol: GRPC} }

// ReadyDef overrides the health check for an ingress.
type ReadyDef struct {
	Type     string        // "tcp", "http", "grpc"
	Path     string        // HTTP check path
	Interval time.Duration // poll interval
	Timeout  time.Duration // max wait
}

// Internal types — used by service builders but not exposed to users.

type egressDef struct {
	service string
	ingress string
}

type hooksDef struct {
	prestart hook
	init     hook
}

type hook interface {
	rigHook()
}

type hookFunc func(ctx context.Context, w Wiring) error

func (hookFunc) rigHook() {}

// Option configures the behavior of Up.
type Option func(*options)

type options struct {
	serverURL      string
	startupTimeout time.Duration
}

func defaultOptions() options {
	return options{
		serverURL:      os.Getenv("RIG_SERVER_ADDR"),
		startupTimeout: 2 * time.Minute,
	}
}

// WithServer sets the rigd server base URL (e.g. "http://127.0.0.1:8080").
// Defaults to the RIG_SERVER_ADDR environment variable.
func WithServer(url string) Option {
	return func(o *options) { o.serverURL = url }
}

// WithTimeout sets the maximum time to wait for the environment to become
// ready. Default is 2 minutes.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.startupTimeout = d }
}

// Up creates an environment, blocks until all services are ready, and
// registers cleanup with t.Cleanup to tear down the environment when the
// test finishes.
//
// If any step fails (server connection, spec validation, service startup),
// Up calls t.Fatal with a descriptive error message.
func Up(t testing.TB, services Services, opts ...Option) *Environment {
	t.Helper()

	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	if o.serverURL == "" {
		t.Fatal("rig: no server address; set RIG_SERVER_ADDR or use rig.WithServer()")
	}

	// Trim trailing slash for consistent URL construction.
	o.serverURL = strings.TrimRight(o.serverURL, "/")

	// Collect hook handlers during spec conversion.
	handlers := make(map[string]hookFunc)
	specEnv, err := envToSpec(t.Name(), services, handlers)
	if err != nil {
		t.Fatalf("rig: build spec: %v", err)
	}

	// POST /environments
	body, err := json.Marshal(specEnv)
	if err != nil {
		t.Fatalf("rig: marshal spec: %v", err)
	}

	resp, err := http.Post(
		o.serverURL+"/environments",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("rig: create environment: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var result struct {
			ValidationErrors []string `json:"validation_errors"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		t.Fatalf("rig: spec validation failed:\n  %s",
			strings.Join(result.ValidationErrors, "\n  "))
	}

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("rig: create environment: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("rig: decode create response: %v", err)
	}

	envID := created.ID

	// Register cleanup: DELETE the environment on test completion.
	t.Cleanup(func() {
		destroyEnvironment(o.serverURL, envID)
	})

	// Open SSE stream and process events until environment.up or failure.
	ctx, cancel := context.WithTimeout(context.Background(), o.startupTimeout)
	defer cancel()

	resolved, err := streamUntilReady(ctx, t, o.serverURL, envID, handlers)
	if err != nil {
		t.Fatalf("rig: %v", err)
	}

	resolved.ID = envID
	resolved.Name = t.Name()

	return resolved
}

// destroyEnvironment sends DELETE /environments/{id}. Blocks until teardown
// completes. Errors are swallowed — cleanup must not abort other tests.
func destroyEnvironment(serverURL, envID string) {
	req, err := http.NewRequest(
		http.MethodDelete,
		fmt.Sprintf("%s/environments/%s", serverURL, envID),
		nil,
	)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}
