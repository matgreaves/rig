package echo_test

import (
	"io"
	"net/http"
	"testing"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
)

func TestEcho(t *testing.T) {
	env := rig.Up(t, rig.Services{
		"echo": rig.Go("./cmd/echo"),
	})

	api := httpx.New(env.Endpoint("echo"))

	resp, err := api.Get("/hello?name=rig")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello, rig!" {
		t.Errorf("body = %q, want %q", string(body), "Hello, rig!")
	}
}
