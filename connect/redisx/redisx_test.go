package redisx_test

import (
	"context"
	"testing"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/redisx"
)

func TestURL(t *testing.T) {
	ep := connect.Endpoint{
		HostPort: "127.0.0.1:6379",
		Protocol: connect.TCP,
		Attributes: map[string]any{
			"REDIS_URL": "redis://127.0.0.1:6379/3",
		},
	}
	if got := redisx.URL(ep); got != "redis://127.0.0.1:6379/3" {
		t.Errorf("URL = %q, want redis://127.0.0.1:6379/3", got)
	}
}

func TestURL_Missing(t *testing.T) {
	ep := connect.Endpoint{HostPort: "127.0.0.1:6379"}
	if got := redisx.URL(ep); got != "" {
		t.Errorf("URL = %q, want empty", got)
	}
}

func TestConnect(t *testing.T) {
	t.Parallel()

	env := rig.Up(t, rig.Services{
		"redis": rig.Redis(),
	})

	rdb := redisx.Connect(env.Endpoint("redis"))
	defer rdb.Close()

	ctx := context.Background()

	// Verify the client works by setting and getting a key.
	if err := rdb.Set(ctx, "test-key", "hello", 0).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	got, err := rdb.Get(ctx, "test-key").Result()
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if got != "hello" {
		t.Errorf("GET = %q, want hello", got)
	}
}
