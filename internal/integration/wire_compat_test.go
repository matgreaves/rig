package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/internal/spec"
)

// TestWireTypeRoundTrip verifies that the JSON produced by the client SDK's
// wire types (client/wire_types.go) can be correctly decoded by the server's
// spec types (internal/spec/). This catches drift between the two copies.
//
// It works by intercepting the POST /environments body the client sends, then
// decoding it with spec.DecodeEnvironment and checking every field.
func TestWireTypeRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var capturedBody []byte

	// Mock server: captures the POST body, then sends environment.up via SSE.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/environments":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			capturedBody = body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"id": "wire-roundtrip"})

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "no flusher", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			// Send environment.up with ingresses matching our service definitions.
			ingresses := map[string]any{
				"mygo":        map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10001, "protocol": "http"}},
				"myprocess":   map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10002, "protocol": "http"}},
				"mycontainer": map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10003, "protocol": "http"}},
				"mypostgres":  map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10004, "protocol": "tcp"}},
				"mytemporal": map[string]any{
					"default": map[string]any{"host": "127.0.0.1", "port": 10005, "protocol": "grpc"},
					"ui":      map[string]any{"host": "127.0.0.1", "port": 10006, "protocol": "http"},
				},
				"mycustom": map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10007, "protocol": "http"}},
				"myfunc":   map[string]any{"default": map[string]any{"host": "127.0.0.1", "port": 10008, "protocol": "http"}},
			}
			data, _ := json.Marshal(map[string]any{
				"type":      "environment.up",
				"ingresses": ingresses,
			})
			fmt.Fprintf(w, "event: lifecycle\ndata: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case r.Method == http.MethodDelete:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{})
		}
	}))
	defer ts.Close()

	// Build environment exercising every wire type field.
	rig.Up(t, rig.Services{
		"mygo": rig.Go("/tmp/fake-module").
			Args("-flag1", "val1").
			EgressAs("db", "mypostgres").
			Ingress("default", rig.IngressDef{
				Protocol: rig.HTTP,
				Ready: &rig.ReadyDef{
					Type:     "http",
					Path:     "/healthz",
					Interval: 500 * time.Millisecond,
					Timeout:  30 * time.Second,
				},
				Attributes: map[string]any{"CUSTOM_KEY": "custom_val"},
			}).
			InitHook(func(ctx context.Context, w rig.Wiring) error { return nil }).
			PrestartHook(func(ctx context.Context, w rig.Wiring) error { return nil }),
		"myprocess": rig.Process("/tmp/fake-bin").Dir("/tmp/workdir"),
		"mycontainer": rig.Container("nginx:alpine").
			Port(80).
			Cmd("sh", "-c", "echo hi").
			Env("FOO", "bar").
			Exec("sh", "-c", "echo test"),
		"mypostgres": rig.Postgres().
			Image("postgres:15").
			InitSQL("CREATE TABLE t (id INT)", "INSERT INTO t VALUES (1)"),
		"mytemporal": rig.Temporal().Version("1.5.1").Namespace("test-ns"),
		"mycustom":   rig.Custom("mytype", map[string]any{"key": "val"}).Args("-x"),
		"myfunc":     rig.Func(func(ctx context.Context) error { return nil }),
	}, rig.WithServer(ts.URL), rig.WithTimeout(5*time.Second), rig.WithObserve())

	// --- Decode captured body with spec types ---

	mu.Lock()
	body := capturedBody
	mu.Unlock()

	if len(body) == 0 {
		t.Fatal("no request body captured")
	}

	env, err := spec.DecodeEnvironment(body)
	if err != nil {
		t.Fatalf("spec.DecodeEnvironment failed: %v", err)
	}

	// --- Environment-level fields ---

	if !env.Observe {
		t.Error("observe flag lost in round-trip")
	}

	expectedServices := []string{"mygo", "myprocess", "mycontainer", "mypostgres", "mytemporal", "mycustom", "myfunc"}
	for _, name := range expectedServices {
		if _, ok := env.Services[name]; !ok {
			t.Errorf("service %q missing from decoded spec", name)
		}
	}

	// --- Go service ---
	{
		svc := env.Services["mygo"]
		if svc.Type != "go" {
			t.Errorf("mygo type = %q, want go", svc.Type)
		}

		var cfg map[string]string
		json.Unmarshal(svc.Config, &cfg)
		if cfg["module"] != "/tmp/fake-module" {
			t.Errorf("mygo config.module = %q, want /tmp/fake-module", cfg["module"])
		}

		if len(svc.Args) != 2 || svc.Args[0] != "-flag1" || svc.Args[1] != "val1" {
			t.Errorf("mygo args = %v, want [-flag1 val1]", svc.Args)
		}

		ing, ok := svc.Ingresses["default"]
		if !ok {
			t.Fatal("mygo missing default ingress")
		}
		if ing.Protocol != spec.HTTP {
			t.Errorf("mygo ingress protocol = %q, want http", ing.Protocol)
		}
		if ing.Ready == nil {
			t.Fatal("mygo ingress ready spec lost")
		}
		if ing.Ready.Type != "http" || ing.Ready.Path != "/healthz" {
			t.Errorf("mygo ready = {%s %s}, want {http /healthz}", ing.Ready.Type, ing.Ready.Path)
		}
		if ing.Ready.Interval.Duration != 500*time.Millisecond {
			t.Errorf("mygo ready.interval = %v, want 500ms", ing.Ready.Interval.Duration)
		}
		if ing.Ready.Timeout.Duration != 30*time.Second {
			t.Errorf("mygo ready.timeout = %v, want 30s", ing.Ready.Timeout.Duration)
		}
		if v, ok := ing.Attributes["CUSTOM_KEY"]; !ok || fmt.Sprint(v) != "custom_val" {
			t.Errorf("mygo ingress attributes[CUSTOM_KEY] = %v, want custom_val", v)
		}

		eg, ok := svc.Egresses["db"]
		if !ok {
			t.Fatal("mygo missing egress 'db'")
		}
		if eg.Service != "mypostgres" {
			t.Errorf("mygo egress.service = %q, want mypostgres", eg.Service)
		}

		if svc.Hooks == nil {
			t.Fatal("mygo hooks lost")
		}
		if len(svc.Hooks.Prestart) != 1 || svc.Hooks.Prestart[0].Type != "client_func" {
			t.Errorf("mygo prestart hook lost or wrong type")
		}
		if svc.Hooks.Prestart[0].ClientFunc == nil || svc.Hooks.Prestart[0].ClientFunc.Name == "" {
			t.Error("mygo prestart hook client_func name empty")
		}
		if len(svc.Hooks.Init) != 1 || svc.Hooks.Init[0].Type != "client_func" {
			t.Errorf("mygo init hook lost or wrong type")
		}
	}

	// --- Process service ---
	{
		svc := env.Services["myprocess"]
		if svc.Type != "process" {
			t.Errorf("myprocess type = %q, want process", svc.Type)
		}
		var cfg map[string]string
		json.Unmarshal(svc.Config, &cfg)
		if cfg["command"] != "/tmp/fake-bin" {
			t.Errorf("myprocess config.command = %q", cfg["command"])
		}
		if cfg["dir"] != "/tmp/workdir" {
			t.Errorf("myprocess config.dir = %q, want /tmp/workdir", cfg["dir"])
		}
	}

	// --- Container service ---
	{
		svc := env.Services["mycontainer"]
		if svc.Type != "container" {
			t.Errorf("mycontainer type = %q, want container", svc.Type)
		}
		var cfg map[string]any
		json.Unmarshal(svc.Config, &cfg)
		if cfg["image"] != "nginx:alpine" {
			t.Errorf("mycontainer config.image = %v", cfg["image"])
		}
		if cmd, ok := cfg["cmd"].([]any); !ok || len(cmd) != 3 {
			t.Errorf("mycontainer config.cmd = %v", cfg["cmd"])
		}
		if envMap, ok := cfg["env"].(map[string]any); !ok || envMap["FOO"] != "bar" {
			t.Errorf("mycontainer config.env = %v", cfg["env"])
		}
		if ing := svc.Ingresses["default"]; ing.ContainerPort != 80 {
			t.Errorf("mycontainer container_port = %d, want 80", ing.ContainerPort)
		}
		// Exec hook should be present as an init hook with type "exec".
		if svc.Hooks == nil || len(svc.Hooks.Init) != 1 {
			t.Fatal("mycontainer exec hook lost")
		}
		if svc.Hooks.Init[0].Type != "exec" {
			t.Errorf("mycontainer init hook type = %q, want exec", svc.Hooks.Init[0].Type)
		}
		var execCfg map[string]any
		json.Unmarshal(svc.Hooks.Init[0].Config, &execCfg)
		if cmd, ok := execCfg["command"].([]any); !ok || len(cmd) != 3 {
			t.Errorf("mycontainer exec hook command = %v", execCfg["command"])
		}
	}

	// --- Postgres service ---
	{
		svc := env.Services["mypostgres"]
		if svc.Type != "postgres" {
			t.Errorf("mypostgres type = %q, want postgres", svc.Type)
		}
		var cfg map[string]string
		json.Unmarshal(svc.Config, &cfg)
		if cfg["image"] != "postgres:15" {
			t.Errorf("mypostgres config.image = %q, want postgres:15", cfg["image"])
		}
		if ing := svc.Ingresses["default"]; ing.Protocol != spec.TCP || ing.ContainerPort != 5432 {
			t.Errorf("mypostgres default ingress = {%s %d}, want {tcp 5432}", ing.Protocol, ing.ContainerPort)
		}
		// InitSQL produces a sql hook.
		if svc.Hooks == nil || len(svc.Hooks.Init) != 1 {
			t.Fatal("mypostgres sql hook lost")
		}
		if svc.Hooks.Init[0].Type != "sql" {
			t.Errorf("mypostgres init hook type = %q, want sql", svc.Hooks.Init[0].Type)
		}
		var sqlCfg map[string]any
		json.Unmarshal(svc.Hooks.Init[0].Config, &sqlCfg)
		stmts, ok := sqlCfg["statements"].([]any)
		if !ok || len(stmts) != 2 {
			t.Errorf("mypostgres sql statements = %v, want 2 items", sqlCfg["statements"])
		}
	}

	// --- Temporal service ---
	{
		svc := env.Services["mytemporal"]
		if svc.Type != "temporal" {
			t.Errorf("mytemporal type = %q, want temporal", svc.Type)
		}
		var cfg map[string]string
		json.Unmarshal(svc.Config, &cfg)
		if cfg["version"] != "1.5.1" {
			t.Errorf("mytemporal config.version = %q, want 1.5.1", cfg["version"])
		}
		if cfg["namespace"] != "test-ns" {
			t.Errorf("mytemporal config.namespace = %q, want test-ns", cfg["namespace"])
		}
		if _, ok := svc.Ingresses["default"]; !ok {
			t.Error("mytemporal missing default ingress")
		}
		if svc.Ingresses["default"].Protocol != spec.GRPC {
			t.Errorf("mytemporal default protocol = %q, want grpc", svc.Ingresses["default"].Protocol)
		}
		if _, ok := svc.Ingresses["ui"]; !ok {
			t.Error("mytemporal missing ui ingress")
		}
		if svc.Ingresses["ui"].Protocol != spec.HTTP {
			t.Errorf("mytemporal ui protocol = %q, want http", svc.Ingresses["ui"].Protocol)
		}
	}

	// --- Custom service ---
	{
		svc := env.Services["mycustom"]
		if svc.Type != "mytype" {
			t.Errorf("mycustom type = %q, want mytype", svc.Type)
		}
		var cfg map[string]any
		json.Unmarshal(svc.Config, &cfg)
		if cfg["key"] != "val" {
			t.Errorf("mycustom config.key = %v, want val", cfg["key"])
		}
		if len(svc.Args) != 1 || svc.Args[0] != "-x" {
			t.Errorf("mycustom args = %v, want [-x]", svc.Args)
		}
	}

	// --- Func service (client type) ---
	{
		svc := env.Services["myfunc"]
		if svc.Type != "client" {
			t.Errorf("myfunc type = %q, want client", svc.Type)
		}
		var cfg map[string]string
		json.Unmarshal(svc.Config, &cfg)
		if cfg["start_handler"] == "" {
			t.Error("myfunc config.start_handler empty")
		}
	}
}

// TestProtocolParity verifies that the Protocol string constants in connect/
// (root module) and internal/spec/ (internal module) have identical values.
// These are defined independently — if they drift, the wire format breaks.
func TestProtocolParity(t *testing.T) {
	cases := []struct {
		name       string
		connectVal connect.Protocol
		specVal    spec.Protocol
	}{
		{"TCP", connect.TCP, spec.TCP},
		{"HTTP", connect.HTTP, spec.HTTP},
		{"GRPC", connect.GRPC, spec.GRPC},
	}
	for _, tc := range cases {
		if string(tc.connectVal) != string(tc.specVal) {
			t.Errorf("Protocol %s: connect=%q spec=%q", tc.name, tc.connectVal, tc.specVal)
		}
	}

	// Also verify spec hasn't added protocols that connect doesn't know about.
	specProtos := spec.ValidProtocols()
	connectKnown := map[string]bool{
		string(connect.TCP):  true,
		string(connect.HTTP): true,
		string(connect.GRPC): true,
	}
	for _, p := range specProtos {
		if !connectKnown[string(p)] {
			t.Errorf("spec defines protocol %q not present in connect package", p)
		}
	}
}
