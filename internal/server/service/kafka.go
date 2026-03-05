package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/matgreaves/rig/internal/server/artifact"
	"github.com/matgreaves/rig/internal/spec"
	"github.com/matgreaves/run"
)

const kafkaDefaultImage = "redpandadata/redpanda:latest"

// KafkaConfig is the type-specific config for "kafka" services.
type KafkaConfig struct {
	Image string `json:"image,omitempty"`
}

// Kafka implements Type, ArtifactProvider, and Initializer for the "kafka"
// builtin service type. Each test gets a fresh Redpanda container (no pool).
type Kafka struct{}

// Artifacts returns a DockerPull artifact for the Redpanda image.
func (Kafka) Artifacts(params ArtifactParams) ([]artifact.Artifact, error) {
	image := kafkaImage(params.Spec.Config)
	return []artifact.Artifact{{
		Key:      "docker:" + image,
		Resolver: artifact.DockerPull{Image: image},
	}}, nil
}

// Publish resolves ingress endpoints using host-allocated ports.
func (Kafka) Publish(ctx context.Context, params PublishParams) (map[string]spec.Endpoint, error) {
	return PublishLocalEndpoints(params)
}

// Runner builds a ContainerConfig and delegates to Container{}.Runner.
func (Kafka) Runner(params StartParams) run.Runner {
	image := kafkaImage(params.Spec.Config)

	cfg := ContainerConfig{
		Image: image,
		Cmd: []string{
			"redpanda", "start",
			"--mode", "dev-container",
			"--kafka-addr", "0.0.0.0:9092",
			"--schema-registry-addr", "0.0.0.0:8081",
		},
	}
	cfgJSON, _ := json.Marshal(cfg)

	modified := params
	modified.Spec.Config = cfgJSON

	return Container{}.Runner(modified)
}

// Init handles server-side init hooks for the Kafka service type.
// Supports the "schema" hook type — registers a schema with the schema registry.
func (Kafka) Init(ctx context.Context, params InitParams) error {
	if params.Hook.Type != "schema" {
		return fmt.Errorf("kafka: unsupported hook type %q", params.Hook.Type)
	}

	var cfg struct {
		Subject    string `json:"subject"`
		SchemaType string `json:"schema_type"`
		Schema     string `json:"schema"`
	}
	if err := json.Unmarshal(params.Hook.Config, &cfg); err != nil {
		return fmt.Errorf("kafka init: invalid schema hook config: %w", err)
	}

	// Find schema-registry ingress.
	ep, ok := params.Ingresses["schema-registry"]
	if !ok {
		return fmt.Errorf("kafka init: no schema-registry ingress found")
	}

	// POST to schema registry.
	url := fmt.Sprintf("http://%s/subjects/%s/versions", ep.HostPort, cfg.Subject)
	body, _ := json.Marshal(map[string]string{
		"schemaType": cfg.SchemaType,
		"schema":     cfg.Schema,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("kafka init: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/vnd.schemaregistry.v1+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("kafka init: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kafka init: POST %s: %d: %s", url, resp.StatusCode, respBody)
	}

	return nil
}

// kafkaImage returns the configured image or the default.
func kafkaImage(raw json.RawMessage) string {
	if raw != nil {
		var cfg KafkaConfig
		if err := json.Unmarshal(raw, &cfg); err == nil && cfg.Image != "" {
			return cfg.Image
		}
	}
	return kafkaDefaultImage
}
