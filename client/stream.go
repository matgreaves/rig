package rig

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// wireEvent mirrors the server's Event type for JSON decoding from the SSE
// stream. Only the fields the SDK needs are included.
type wireEvent struct {
	Type      string                                `json:"type"`
	Service   string                                `json:"service,omitempty"`
	Ingress   string                                `json:"ingress,omitempty"`
	Artifact  string                                `json:"artifact,omitempty"`
	Error     string                                `json:"error,omitempty"`
	Log       *wireLogEntry                         `json:"log,omitempty"`
	Callback  *wireCallbackRequest                  `json:"callback,omitempty"`
	Ingresses map[string]map[string]wireEndpoint    `json:"ingresses,omitempty"`
}

type wireLogEntry struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type wireCallbackRequest struct {
	RequestID string             `json:"request_id"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	Wiring    *wireWiringContext  `json:"wiring,omitempty"`
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

type wireCallbackResponse struct {
	RequestID string         `json:"request_id"`
	Error     string         `json:"error,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// streamUntilReady connects to the SSE stream and processes events until
// environment.up arrives (success) or environment.down arrives (failure).
func streamUntilReady(
	ctx context.Context,
	t testing.TB,
	serverURL string,
	envID string,
	handlers map[string]hookFunc,
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

			result, done, err := handleEvent(ctx, t, serverURL, envID, ev, handlers)
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
func handleEvent(
	ctx context.Context,
	t testing.TB,
	serverURL string,
	envID string,
	ev wireEvent,
	handlers map[string]hookFunc,
) (*Environment, bool, error) {
	switch ev.Type {
	case "callback.request":
		if ev.Callback == nil {
			return nil, false, nil
		}
		if err := dispatchCallback(ctx, serverURL, envID, ev.Callback, handlers); err != nil {
			return nil, false, fmt.Errorf("callback %q: %w", ev.Callback.Name, err)
		}

	case "environment.up":
		resolved := buildEnvironmentFromEvent(ev)
		return resolved, true, nil

	case "environment.down":
		return nil, false, fmt.Errorf("environment failed (received environment.down without environment.up)")

	case "service.log":
		if ev.Log != nil {
			t.Logf("rig: %s | %s", ev.Service, strings.TrimRight(ev.Log.Data, "\n"))
		}

	case "service.failed":
		t.Logf("rig: service %q failed: %s", ev.Service, ev.Error)

	case "artifact.started":
		t.Logf("rig: resolving artifact %q", ev.Artifact)

	case "artifact.cached":
		t.Logf("rig: artifact %q (cached)", ev.Artifact)

	case "artifact.completed":
		t.Logf("rig: artifact %q resolved", ev.Artifact)

	case "artifact.failed":
		t.Logf("rig: artifact %q failed: %s", ev.Artifact, ev.Error)
	}

	return nil, false, nil
}

// dispatchCallback finds the registered handler, calls it, and POSTs the result.
func dispatchCallback(
	ctx context.Context,
	serverURL string,
	envID string,
	cb *wireCallbackRequest,
	handlers map[string]hookFunc,
) error {
	handler, ok := handlers[cb.Name]
	if !ok {
		// Post error back so the server lifecycle doesn't hang forever.
		postCallbackResult(serverURL, envID, cb.RequestID,
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

	if err := postCallbackResult(serverURL, envID, cb.RequestID, handlerErr); err != nil {
		return err
	}
	return handlerErr
}

// postCallbackResult POSTs the callback response to the server.
func postCallbackResult(serverURL, envID, requestID string, handlerErr error) error {
	payload := wireCallbackResponse{RequestID: requestID}
	if handlerErr != nil {
		payload.Error = handlerErr.Error()
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/environments/%s/callbacks/%s", serverURL, envID, requestID)

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post callback result: %w", err)
	}
	resp.Body.Close()
	return nil
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
