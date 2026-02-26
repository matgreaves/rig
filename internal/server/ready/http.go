package ready

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HTTP checks readiness by making an HTTP GET request.
// Any response with status < 500 is considered ready.
type HTTP struct {
	Path string // default "/"
}

func (h *HTTP) Check(ctx context.Context, addr string) error {
	path := h.Path
	if path == "" {
		path = "/"
	}

	url := fmt.Sprintf("http://%s%s", addr, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
