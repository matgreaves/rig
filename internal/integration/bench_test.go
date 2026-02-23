package integration_test

import (
	"io"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/internal/testdata/services/echo"
)

// BenchmarkRequestThroughput measures HTTP request throughput through a
// rig-managed echo service, comparing direct (observe=false) vs proxied
// (observe=true) paths.
func BenchmarkRequestThroughput(b *testing.B) {
	serverURL := sharedServerURL

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
