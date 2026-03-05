package rig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// KafkaDef defines a service backed by the builtin Kafka type (Redpanda).
// Each test gets a fresh container — no pool, no topic collision.
//
// The service exposes two ingresses:
//   - "default" (Kafka protocol on port 9092) — use ep.HostPort as bootstrap servers
//   - "schema-registry" (HTTP on port 8081) — Confluent-compatible schema registry
//
// From tests, access them via the environment:
//
//	ep := env.Endpoint("kafka")                        // bootstrap servers
//	sr := env.Endpoint("kafka", "schema-registry")     // schema registry
//
// Services that depend on Kafka can wire both ingresses as separate egresses:
//
//	rig.Go("./cmd/worker").
//	    Egress("kafka").                                       // → default ingress
//	    EgressAs("schema-registry", "kafka", "schema-registry") // → schema-registry ingress
type KafkaDef struct {
	image    string
	egresses map[string]egressDef
	hooks    hooksDef
}

func (*KafkaDef) rigService() {}

// Kafka creates a Kafka service definition using Redpanda.
//
//	rig.Kafka()
//	rig.Kafka().Image("redpandadata/redpanda:v24.1.1")
//	rig.Kafka().AvroSchema("schemas/user-value.avsc")
func Kafka() *KafkaDef {
	return &KafkaDef{}
}

// Image overrides the default Redpanda Docker image.
func (d *KafkaDef) Image(image string) *KafkaDef {
	d.image = image
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *KafkaDef) Egress(service string) *KafkaDef {
	return d.EgressAs(service, service)
}

// EgressAs adds a dependency with a custom local name.
func (d *KafkaDef) EgressAs(name, service string, ingress ...string) *KafkaDef {
	if d.egresses == nil {
		d.egresses = make(map[string]egressDef)
	}
	eg := egressDef{service: service}
	if len(ingress) > 0 {
		eg.ingress = ingress[0]
	}
	d.egresses[name] = eg
	return d
}

// InitHook registers a client-side init hook function.
func (d *KafkaDef) InitHook(fn func(ctx context.Context, w Wiring) error) *KafkaDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *KafkaDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *KafkaDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}

// AvroSchema registers an Avro schema file to be posted to the schema registry
// during init. The subject name is derived from the filename (sans extension):
// "user-value.avsc" → subject "user-value".
//
// The file is read at call time. Panics if the file cannot be read.
func (d *KafkaDef) AvroSchema(path string) *KafkaDef {
	return d.addSchema(path, "AVRO")
}

// ProtoSchema registers a Protobuf schema file to be posted to the schema
// registry during init. The subject name is derived from the filename (sans
// extension): "order-key.proto" → subject "order-key".
//
// The file is read at call time. Panics if the file cannot be read.
func (d *KafkaDef) ProtoSchema(path string) *KafkaDef {
	return d.addSchema(path, "PROTOBUF")
}

func (d *KafkaDef) addSchema(path, schemaType string) *KafkaDef {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			panic("rig: schema: " + err.Error())
		}
		path = filepath.Join(wd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		panic("rig: schema: " + err.Error())
	}
	subject := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	d.hooks.init = append(d.hooks.init, schemaHook{
		subject:    subject,
		schemaType: schemaType,
		schema:     string(data),
	})
	return d
}
