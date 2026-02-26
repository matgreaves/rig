package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// runGRPC starts an HTTP/2 cleartext reverse proxy that captures gRPC metadata.
// Structurally identical to runHTTP but uses h2c for HTTP/2 without TLS.
func (f *Forwarder) runGRPC(ctx context.Context) error {
	target := &url.URL{
		Scheme: "http",
		Host:   f.targetAddr(),
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // streaming support
	proxy.Transport = &observingTransport{
		inner: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		emit:       f.Emit,
		source:     f.Source,
		target:     f.TargetSvc,
		ingress:    f.Ingress,
		getDecoder: func() *GRPCDecoder { return f.Decoder },
	}

	ln, err := f.getListener()
	if err != nil {
		return fmt.Errorf("proxy %s→%s: listen: %w", f.Source, f.TargetSvc, err)
	}

	h2s := &http2.Server{}
	handler := h2c.NewHandler(proxy, h2s)
	srv := &http.Server{Handler: handler}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
