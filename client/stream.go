package rig

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/matgreaves/rig/connect"
)

// wireEvent mirrors the server's Event type for JSON decoding from the SSE
// stream. Only the fields the SDK needs are included.
type wireEvent struct {
	Type       string                             `json:"type"`
	Service    string                             `json:"service,omitempty"`
	Ingress    string                             `json:"ingress,omitempty"`
	Artifact   string                             `json:"artifact,omitempty"`
	Error      string                             `json:"error,omitempty"`
	Log        *wireLogEntry                      `json:"log,omitempty"`
	Callback   *wireCallbackRequest               `json:"callback,omitempty"`
	Request    *wireRequestInfo                   `json:"request,omitempty"`
	Connection *wireConnectionInfo                `json:"connection,omitempty"`
	Ingresses  map[string]map[string]wireEndpoint `json:"ingresses,omitempty"`
}

type wireRequestInfo struct {
	Source       string  `json:"source"`
	Target       string  `json:"target"`
	Ingress      string  `json:"ingress"`
	Method       string  `json:"method"`
	Path         string  `json:"path"`
	StatusCode   int     `json:"status_code"`
	LatencyMs    float64 `json:"latency_ms"`
	RequestSize  int64   `json:"request_size"`
	ResponseSize int64   `json:"response_size"`
}

type wireConnectionInfo struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Ingress    string  `json:"ingress"`
	BytesIn    int64   `json:"bytes_in"`
	BytesOut   int64   `json:"bytes_out"`
	DurationMs float64 `json:"duration_ms"`
}

type wireLogEntry struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type wireCallbackRequest struct {
	RequestID string             `json:"request_id"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	Wiring    *wireWiringContext `json:"wiring,omitempty"`
}

type wireWiringContext struct {
	Ingresses  map[string]wireEndpoint `json:"ingresses,omitempty"`
	Egresses   map[string]wireEndpoint `json:"egresses,omitempty"`
	TempDir    string                  `json:"temp_dir,omitempty"`
	EnvDir     string                  `json:"env_dir,omitempty"`
	Attributes map[string]string       `json:"attributes,omitempty"`
}

type wireEndpoint struct {
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	Protocol   Protocol       `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// streamUntilReady connects to the SSE stream and processes events until
// environment.up arrives (success) or environment.down arrives (failure).
// funcCtx is the context for client-side functions (cancelled during cleanup).
// startHandlers maps start callback names to functions launched asynchronously.
func streamUntilReady(
	ctx context.Context,
	serverURL string,
	envID string,
	handlers map[string]hookFunc,
	funcCtx context.Context,
	startHandlers map[string]startFunc,
) (*Environment, error) {
	url := fmt.Sprintf("%s/environments/%s/events", serverURL, envID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create SSE request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to event stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("event stream: HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType, data string
	var failures []string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")

		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")

		case line == "":
			if eventType == "" || data == "" {
				eventType, data = "", ""
				continue
			}

			var ev wireEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				eventType, data = "", ""
				continue
			}

			result, done, err := handleEvent(ctx, serverURL, envID, ev, handlers, funcCtx, startHandlers, &failures)
			if err != nil {
				return nil, err
			}
			if done {
				return result, nil
			}

			eventType, data = "", ""
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("event stream read: %w", err)
	}

	return nil, fmt.Errorf("event stream closed before environment.up")
}

// handleEvent processes a single SSE event. Returns (result, done, error).
// Failures from service.failed and artifact.failed events are accumulated
// and included in the environment.down error.
func handleEvent(
	ctx context.Context,
	serverURL string,
	envID string,
	ev wireEvent,
	handlers map[string]hookFunc,
	funcCtx context.Context,
	startHandlers map[string]startFunc,
	failures *[]string,
) (*Environment, bool, error) {
	switch ev.Type {
	case "callback.request":
		if ev.Callback == nil {
			return nil, false, nil
		}
		if ev.Callback.Type == "start" {
			if err := dispatchStartCallback(funcCtx, serverURL, envID, ev.Service, ev.Callback, startHandlers); err != nil {
				return nil, false, fmt.Errorf("start callback %q: %w", ev.Callback.Name, err)
			}
		} else {
			if err := dispatchHookCallback(ctx, serverURL, envID, ev.Service, ev.Callback, handlers); err != nil {
				return nil, false, fmt.Errorf("callback %q: %w", ev.Callback.Name, err)
			}
		}

	case "environment.up":
		resolved := buildEnvironmentFromEvent(ev)
		return resolved, true, nil

	case "environment.down":
		if len(*failures) > 0 {
			return nil, false, fmt.Errorf("environment failed:\n  %s", strings.Join(*failures, "\n  "))
		}
		return nil, false, fmt.Errorf("environment failed")

	case "service.failed":
		*failures = append(*failures, fmt.Sprintf("service %q: %s", ev.Service, ev.Error))

	case "artifact.failed":
		*failures = append(*failures, fmt.Sprintf("artifact %q: %s", ev.Artifact, ev.Error))
	}

	return nil, false, nil
}

// dispatchHookCallback finds the registered hook handler, calls it synchronously,
// and POSTs the result.
func dispatchHookCallback(
	ctx context.Context,
	serverURL string,
	envID string,
	serviceName string,
	cb *wireCallbackRequest,
	handlers map[string]hookFunc,
) error {
	handler, ok := handlers[cb.Name]
	if !ok {
		postCallbackResult(serverURL, envID, serviceName, cb.RequestID,
			fmt.Errorf("no handler registered for callback %q", cb.Name))
		return fmt.Errorf("no handler registered for callback %q", cb.Name)
	}

	wiring := convertWiring(cb.Wiring)

	var handlerErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				handlerErr = fmt.Errorf("panic in hook handler: %v", r)
			}
		}()
		handlerErr = handler(ctx, wiring)
	}()

	if err := postCallbackResult(serverURL, envID, serviceName, cb.RequestID, handlerErr); err != nil {
		return err
	}
	return handlerErr
}

// dispatchStartCallback launches a client-side function asynchronously and
// immediately posts a success response. The function runs until funcCtx is
// cancelled during cleanup. If the function returns an error before cleanup,
// a service.error event is posted to the server to trigger teardown.
func dispatchStartCallback(
	funcCtx context.Context,
	serverURL string,
	envID string,
	serviceName string,
	cb *wireCallbackRequest,
	startHandlers map[string]startFunc,
) error {
	handler, ok := startHandlers[cb.Name]
	if !ok {
		postCallbackResult(serverURL, envID, serviceName, cb.RequestID,
			fmt.Errorf("no start handler registered for callback %q", cb.Name))
		return fmt.Errorf("no start handler registered for callback %q", cb.Name)
	}

	// Build wiring and inject into context.
	wiring := convertWiring(cb.Wiring)
	wiringCtx := connect.WithWiring(funcCtx, &wiring)

	// Launch the function in a goroutine — it runs until funcCtx is cancelled.
	go func() {
		if err := handler(wiringCtx); err != nil && funcCtx.Err() == nil {
			// Function failed before cleanup — report to server so it can
			// fail the service and tear down the environment.
			postServiceError(serverURL, envID, serviceName, err)
		}
	}()

	// Respond immediately — the function is running.
	return postCallbackResult(serverURL, envID, serviceName, cb.RequestID, nil)
}

// postClientEvent POSTs a client event to the server's unified events endpoint.
func postClientEvent(serverURL, envID string, payload any) error {
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/environments/%s/events", serverURL, envID)

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post client event: %w", err)
	}
	resp.Body.Close()
	return nil
}

// postCallbackResult posts a callback.response event to the server.
func postCallbackResult(serverURL, envID, serviceName, requestID string, handlerErr error) error {
	payload := struct {
		Type      string `json:"type"`
		Service   string `json:"service"`
		RequestID string `json:"request_id"`
		Error     string `json:"error,omitempty"`
	}{
		Type:      "callback.response",
		Service:   serviceName,
		RequestID: requestID,
	}
	if handlerErr != nil {
		payload.Error = handlerErr.Error()
	}
	return postClientEvent(serverURL, envID, payload)
}

// postServiceError posts a service.error event to the server, causing the
// server to mark the service as failed and trigger teardown.
func postServiceError(serverURL, envID, service string, err error) {
	payload := struct {
		Type    string `json:"type"`
		Service string `json:"service"`
		Error   string `json:"error"`
	}{
		Type:    "service.error",
		Service: service,
		Error:   err.Error(),
	}
	postClientEvent(serverURL, envID, payload)
}

// convertWiring converts wire format wiring to SDK Wiring type.
func convertWiring(w *wireWiringContext) Wiring {
	if w == nil {
		return Wiring{}
	}
	return Wiring{
		Ingresses:  convertEndpoints(w.Ingresses),
		Egresses:   convertEndpoints(w.Egresses),
		Attributes: w.Attributes,
		TempDir:    w.TempDir,
		EnvDir:     w.EnvDir,
	}
}

// convertEndpoints converts wireEndpoint maps to SDK Endpoint maps.
func convertEndpoints(eps map[string]wireEndpoint) map[string]Endpoint {
	if eps == nil {
		return nil
	}
	out := make(map[string]Endpoint, len(eps))
	for name, ep := range eps {
		out[name] = Endpoint{
			Host:       ep.Host,
			Port:       ep.Port,
			Protocol:   ep.Protocol,
			Attributes: ep.Attributes,
		}
	}
	return out
}

// buildEnvironmentFromEvent constructs an Environment from an environment.up event.
func buildEnvironmentFromEvent(ev wireEvent) *Environment {
	services := make(map[string]ResolvedService, len(ev.Ingresses))
	for svcName, ingressMap := range ev.Ingresses {
		ingresses := make(map[string]Endpoint, len(ingressMap))
		for ingName, ep := range ingressMap {
			ingresses[ingName] = Endpoint{
				Host:       ep.Host,
				Port:       ep.Port,
				Protocol:   ep.Protocol,
				Attributes: ep.Attributes,
			}
		}
		services[svcName] = ResolvedService{Ingresses: ingresses}
	}
	return &Environment{
		Services: services,
	}
}
