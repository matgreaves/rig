package rig

import "context"

// SQSDef defines a service backed by the builtin SQS type.
// Rig manages the container lifecycle and queue isolation — the API
// is minimal.
//
// Publishes SQS_ENDPOINT, SQS_QUEUE_URL, AWS_ACCESS_KEY_ID, and
// AWS_SECRET_ACCESS_KEY as endpoint attributes.
// Each environment gets an isolated queue assigned by the server.
type SQSDef struct {
	egresses map[string]egressDef
	hooks    hooksDef
}

func (*SQSDef) rigService() {}

// SQS creates an SQS service definition backed by ElasticMQ.
// Each environment gets an isolated queue assigned automatically by the
// server.
//
//	rig.SQS()
func SQS() *SQSDef {
	return &SQSDef{}
}

// Egress adds a dependency on a service, named after the target.
func (d *SQSDef) Egress(service string) *SQSDef {
	return d.EgressAs(service, service)
}

// EgressAs adds a dependency with a custom local name.
func (d *SQSDef) EgressAs(name, service string, ingress ...string) *SQSDef {
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
func (d *SQSDef) InitHook(fn func(ctx context.Context, w Wiring) error) *SQSDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *SQSDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *SQSDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
