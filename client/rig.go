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
// (*GoDef, *FuncDef, *ProcessDef, *CustomDef). It is sealed — only types
// in this package implement it.
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
	prestart []hook
	init     []hook
}

type hook interface {
	rigHook()
}

type hookFunc func(ctx context.Context, w Wiring) error

func (hookFunc) rigHook() {}

type sqlHook struct {
	statements []string
}

func (sqlHook) rigHook() {}

type execHook struct {
	command []string
}

func (execHook) rigHook() {}

// startFunc is a function that runs as a service in the test process.
type startFunc func(ctx context.Context) error

// Option configures the behavior of Up.
type Option func(*options)

type options struct {
	serverURL      string
	startupTimeout time.Duration
	observe        bool
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

// WithObserve enables transparent traffic proxying. When set, rig inserts
// a proxy on every egress edge and every external connection, capturing
// request/connection events in the event log.
func WithObserve() Option {
	return func(o *options) { o.observe = true }
}

// Up creates an environment, blocks until all services are ready, and
// registers cleanup with t.Cleanup to tear down the environment when the
// test finishes.
//
// If any step fails (server connection, spec validation, service startup),
// Up calls t.Fatal with a descriptive error message.
func Up(t testing.TB, services Services, opts ...Option) *Environment {
	t.Helper()
	env, err := up(t, services, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// up is the internal implementation of Up. It returns an error instead of
// calling t.Fatal, making it testable for expected-failure cases.
func up(t testing.TB, services Services, opts ...Option) (*Environment, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	if o.serverURL == "" {
		addr, err := ensureServer("")
		if err != nil {
			return nil, fmt.Errorf("rig: %w", err)
		}
		o.serverURL = addr
	}

	// Trim trailing slash for consistent URL construction.
	o.serverURL = strings.TrimRight(o.serverURL, "/")

	// Collect handlers during spec conversion.
	handlers := make(map[string]hookFunc)
	startHandlers := make(map[string]startFunc)
	specEnv, err := envToSpec(t.Name(), services, handlers, startHandlers, o.observe)
	if err != nil {
		return nil, fmt.Errorf("rig: build spec: %v", err)
	}

	// POST /environments
	body, err := json.Marshal(specEnv)
	if err != nil {
		return nil, fmt.Errorf("rig: marshal spec: %v", err)
	}

	resp, err := http.Post(
		o.serverURL+"/environments",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("rig: create environment: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var result struct {
			ValidationErrors []string `json:"validation_errors"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		return nil, fmt.Errorf("rig: spec validation failed:\n  %s",
			strings.Join(result.ValidationErrors, "\n  "))
	}

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rig: create environment: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("rig: decode create response: %v", err)
	}

	envID := created.ID

	// Create a context for client-side functions. Cancelled during cleanup
	// before the environment is destroyed, giving functions time to stop.
	funcCtx, funcCancel := context.WithCancel(context.Background())

	// Register cleanup: stop functions, destroy the environment.
	// Always write the event log so it's available for inspection.
	t.Cleanup(func() {
		funcCancel()
		logFile := destroyEnvironment(o.serverURL, envID)
		if logFile != "" {
			t.Logf("rig: event log: %s", logFile)
		}
	})

	// Open SSE stream and process events until environment.up or failure.
	ctx, cancel := context.WithTimeout(context.Background(), o.startupTimeout)
	defer cancel()

	resolved, err := streamUntilReady(ctx, o.serverURL, envID, handlers, funcCtx, startHandlers)
	if err != nil {
		return nil, fmt.Errorf("rig: %v", err)
	}

	resolved.ID = envID
	resolved.Name = t.Name()
	resolved.T = &rigTB{
		TB:        t,
		serverURL: o.serverURL,
		envID:     envID,
	}

	return resolved, nil
}

// destroyEnvironment sends DELETE /environments/{id}?log=true. Blocks until
// teardown completes. The server writes the event log to disk and returns the
// path. Errors are swallowed — cleanup must not abort other tests.
func destroyEnvironment(serverURL, envID string) string {
	url := fmt.Sprintf("%s/environments/%s?log=true", serverURL, envID)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		LogFile string `json:"log_file"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.LogFile
}
