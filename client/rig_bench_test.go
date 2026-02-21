package rig_test

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/server"
	"github.com/matgreaves/rig/server/service"
	"github.com/matgreaves/rig/testdata/services/echo"
)

// BenchmarkRequestThroughput measures HTTP request throughput through a
// rig-managed echo service, comparing direct (observe=false) vs proxied
// (observe=true) paths.
func BenchmarkRequestThroughput(b *testing.B) {
	serverURL := benchTestServer(b)

	for _, observe := range []bool{false, true} {
		name := "observe=false"
		if observe {
			name = "observe=true"
		}

		b.Run(name, func(b *testing.B) {
			opts := []rig.Option{
				rig.WithServer(serverURL),
				rig.WithTimeout(60 * time.Second),
			}
			if observe {
				opts = append(opts, rig.WithObserve())
			}

			env := rig.Up(b, rig.Services{
				"echo": rig.Func(echo.Run),
			}, opts...)

			client := httpx.New(env.Endpoint("echo"))

			// Warm up: one request to ensure everything is connected.
			resp, err := client.Get("/health")
			if err != nil {
				b.Fatal(err)
			}
			resp.Body.Close()

			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				resp, err := client.Get("/bench")
				if err != nil {
					b.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		})
	}
}

// benchModuleRoot finds the module root from the working directory.
func benchModuleRoot(tb testing.TB) string {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// benchTestServer creates an httptest.Server backed by a real server.Server.
func benchTestServer(tb testing.TB) string {
	tb.Helper()
	reg := service.NewRegistry()
	reg.Register("process", service.Process{})
	reg.Register("go", service.Go{})
	reg.Register("client", service.Client{})
	reg.Register("container", service.Container{})

	rigDir := filepath.Join(benchModuleRoot(tb), ".rig")
	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		tb.TempDir(),
		0,
		rigDir,
	)
	ts := httptest.NewServer(s)
	tb.Cleanup(ts.Close)
	return ts.URL
}
