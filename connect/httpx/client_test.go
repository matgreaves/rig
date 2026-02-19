package httpx_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matgreaves/rig/connect/httpx"
)

func TestGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/health" {
			t.Errorf("path = %s, want /health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := httpx.NewClient(ts.URL)
	resp, err := client.Get("/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHead(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := httpx.NewClient(ts.URL)
	resp, err := client.Head("/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/orders" {
			t.Errorf("path = %s, want /orders", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %s, want application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"id":1}` {
			t.Errorf("body = %s, want {\"id\":1}", body)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	client := httpx.NewClient(ts.URL)
	resp, err := client.Post("/orders", "application/json", bytes.NewBufferString(`{"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
}

func TestDo_RelativePath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/orders/42" {
			t.Errorf("path = %s, want /orders/42", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := httpx.NewClient(ts.URL)
	req, _ := http.NewRequest("DELETE", "/orders/42", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestDo_AbsoluteURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Client pointed at a different base URL.
	client := httpx.NewClient("http://should-not-be-used:9999")
	// Absolute URL overrides BaseURL.
	req, _ := http.NewRequest("GET", ts.URL+"/override", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCustomHTTPClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "test" {
			t.Error("custom transport header missing")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := httpx.NewClient(ts.URL)
	client.HTTP = &http.Client{
		Transport: &headerTransport{Header: "X-Custom", Value: "test"},
	}

	resp, err := client.Get("/test")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// headerTransport is a test RoundTripper that injects a header.
type headerTransport struct {
	Header string
	Value  string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(t.Header, t.Value)
	return http.DefaultTransport.RoundTrip(req)
}
