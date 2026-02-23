package temporalx_test

import (
	"context"
	"testing"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/temporalx"
	"go.temporal.io/api/workflowservice/v1"
)

func TestAddr(t *testing.T) {
	ep := connect.Endpoint{
		Host:     "127.0.0.1",
		Port:     7233,
		Protocol: connect.GRPC,
		Attributes: map[string]any{
			"TEMPORAL_ADDRESS":   "127.0.0.1:7233",
			"TEMPORAL_NAMESPACE": "default",
		},
	}
	if got := temporalx.Addr(ep); got != "127.0.0.1:7233" {
		t.Errorf("Addr = %q, want 127.0.0.1:7233", got)
	}
}

func TestAddr_Missing(t *testing.T) {
	ep := connect.Endpoint{Host: "127.0.0.1", Port: 7233}
	if got := temporalx.Addr(ep); got != "" {
		t.Errorf("Addr = %q, want empty", got)
	}
}

func TestNamespace(t *testing.T) {
	ep := connect.Endpoint{
		Host:     "127.0.0.1",
		Port:     7233,
		Protocol: connect.GRPC,
		Attributes: map[string]any{
			"TEMPORAL_ADDRESS":   "127.0.0.1:7233",
			"TEMPORAL_NAMESPACE": "my-ns",
		},
	}
	if got := temporalx.Namespace(ep); got != "my-ns" {
		t.Errorf("Namespace = %q, want my-ns", got)
	}
}

func TestNamespace_Missing(t *testing.T) {
	ep := connect.Endpoint{Host: "127.0.0.1", Port: 7233}
	if got := temporalx.Namespace(ep); got != "" {
		t.Errorf("Namespace = %q, want empty", got)
	}
}

func TestDial(t *testing.T) {
	t.Parallel()

	env := rig.Up(t, rig.Services{
		"temporal": rig.Temporal(),
	})

	c, err := temporalx.Dial(env.Endpoint("temporal"))
	if err != nil {
		t.Fatalf("temporalx.Dial: %v", err)
	}
	defer c.Close()

	// Verify the client works by describing the default namespace.
	ns := temporalx.Namespace(env.Endpoint("temporal"))
	resp, err := c.WorkflowService().DescribeNamespace(context.Background(),
		&workflowservice.DescribeNamespaceRequest{Namespace: ns})
	if err != nil {
		t.Fatalf("DescribeNamespace: %v", err)
	}
	if got := resp.NamespaceInfo.GetName(); got != "default" {
		t.Errorf("namespace = %q, want default", got)
	}
}
