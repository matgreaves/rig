package rig_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
)

func TestSmoke(t *testing.T) {
	if _, err := exec.LookPath("rigd"); err != nil {
		if os.Getenv("RIG_BINARY") == "" {
			t.Skip("rigd not available; run via 'make test'")
		}
	}

	env := rig.Up(t, rig.Services{
		"echo": rig.Func(func(ctx context.Context) error {
			return httpx.ListenAndServe(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "ok")
			}))
		}),
	}, rig.WithTimeout(30*time.Second))

	resp, err := http.Get("http://" + env.Endpoint("echo").HostPort + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
}
