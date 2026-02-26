package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/matgreaves/rig/connect"
)

// ListenAndServe reads the default ingress endpoint from the environment
// and starts an HTTP server with the given handler. It blocks until ctx
// is cancelled, then shuts down gracefully.
//
// This is the server-side counterpart to New / NewClient.
//
//	func main() {
//	    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
//	    defer stop()
//	    mux := http.NewServeMux()
//	    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
//	    httpx.ListenAndServe(ctx, mux)
//	}
func ListenAndServe(ctx context.Context, handler http.Handler) error {
	w, err := connect.ParseWiring(ctx)
	if err != nil {
		return err
	}
	return Serve(ctx, w.Ingress(), handler)
}

// Serve starts an HTTP server on the given endpoint with the provided
// handler. It blocks until ctx is cancelled, then shuts down gracefully
// with a 5-second timeout.
func Serve(ctx context.Context, ep connect.Endpoint, handler http.Handler) error {
	srv := &http.Server{
		Addr:    ep.HostPort,
		Handler: handler,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
