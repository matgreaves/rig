package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// maxBodyCapture is the maximum number of body bytes captured per request or
// response for the event log. The full body is always forwarded regardless.
const maxBodyCapture = 64 * 1024 // 64KB

// runHTTP starts an HTTP reverse proxy that captures request metadata.
func (f *Forwarder) runHTTP(ctx context.Context) error {
	target := &url.URL{
		Scheme: "http",
		Host:   f.targetAddr(),
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &observingTransport{
		inner:   http.DefaultTransport,
		emit:    f.Emit,
		source:  f.Source,
		target:  f.TargetSvc,
		ingress: f.Ingress,
	}

	ln, err := net.Listen("tcp", f.listenAddr())
	if err != nil {
		return fmt.Errorf("proxy %s→%s: listen: %w", f.Source, f.TargetSvc, err)
	}

	srv := &http.Server{Handler: proxy}

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

// observingTransport wraps an http.RoundTripper to capture headers and bodies.
type observingTransport struct {
	inner   http.RoundTripper
	emit    func(Event)
	source  string
	target  string
	ingress string
}

func (t *observingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Copy request headers before the transport modifies them.
	reqHeaders := cloneHeaders(req.Header)

	// Tee request body into a capped buffer as the transport reads it.
	reqCapture := &cappedBuffer{max: maxBodyCapture}
	if req.Body != nil {
		req.Body = readCloser{
			Reader: io.TeeReader(req.Body, reqCapture),
			Closer: req.Body,
		}
	}

	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	latency := time.Since(start)

	respHeaders := cloneHeaders(resp.Header)

	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	// Wrap response body to tee into a capped buffer. The event is emitted
	// when the reverse proxy closes the body after streaming to the client.
	respCapture := &cappedBuffer{max: maxBodyCapture}
	resp.Body = &observedBody{
		reader:  io.TeeReader(resp.Body, respCapture),
		closer:  resp.Body,
		capture: respCapture,
		emit: func() {
			t.emit(Event{
				Type: "request.completed",
				Request: &RequestInfo{
					Source:                t.source,
					Target:                t.target,
					Ingress:               t.ingress,
					Method:                req.Method,
					Path:                  path,
					StatusCode:            resp.StatusCode,
					LatencyMs:             float64(latency.Microseconds()) / 1000.0,
					RequestSize:           reqCapture.total,
					ResponseSize:          respCapture.total,
					RequestHeaders:        reqHeaders,
					RequestBody:           reqCapture.bytes(),
					RequestBodyTruncated:  reqCapture.truncated,
					ResponseHeaders:       respHeaders,
					ResponseBody:          respCapture.bytes(),
					ResponseBodyTruncated: respCapture.truncated,
				},
			})
		},
	}

	return resp, nil
}

// cappedBuffer captures up to max bytes written to it, tracking total bytes
// and whether any data was truncated.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
	total     int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if b.truncated {
		return n, nil
	}
	remaining := b.max - b.buf.Len()
	if n <= remaining {
		b.buf.Write(p)
	} else {
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
		b.truncated = true
	}
	return n, nil
}

func (b *cappedBuffer) bytes() []byte {
	if b.buf.Len() == 0 {
		return nil
	}
	return b.buf.Bytes()
}

// observedBody wraps a response body, teeing reads into a capture buffer
// and emitting a traffic event when closed.
type observedBody struct {
	reader  io.Reader
	closer  io.Closer
	capture *cappedBuffer
	emit    func()
	once    sync.Once
}

func (b *observedBody) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *observedBody) Close() error {
	// Drain any unread body so respCapture.total reflects the full size.
	io.Copy(io.Discard, b.reader)
	err := b.closer.Close()
	b.once.Do(b.emit)
	return err
}

// readCloser combines a Reader and Closer into an io.ReadCloser.
type readCloser struct {
	io.Reader
	io.Closer
}

// cloneHeaders returns a deep copy of an http.Header.
func cloneHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	return map[string][]string(h.Clone())
}
