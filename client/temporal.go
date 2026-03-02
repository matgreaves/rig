package rig

import "context"

// TemporalDef defines a service backed by the builtin Temporal type.
// Rig downloads the Temporal CLI binary on first use, caches it, and
// runs `temporal server start-dev` with automatic port wiring.
//
// Publishes TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE as endpoint attributes.
type TemporalDef struct {
	version   string
	namespace string
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*TemporalDef) rigService() {}

// Temporal creates a Temporal service definition. By default uses the latest
// known CLI version and the "default" namespace.
//
//	rig.Temporal()
//	rig.Temporal().Version("1.5.1").Namespace("my-ns")
func Temporal() *TemporalDef {
	return &TemporalDef{}
}

// Version overrides the Temporal CLI version (default: 1.5.1).
func (d *TemporalDef) Version(v string) *TemporalDef {
	d.version = v
	return d
}

// Namespace overrides the default namespace name (default: "default").
func (d *TemporalDef) Namespace(ns string) *TemporalDef {
	d.namespace = ns
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *TemporalDef) Egress(service string, ingress ...string) *TemporalDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *TemporalDef) EgressAs(name, service string, ingress ...string) *TemporalDef {
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
func (d *TemporalDef) InitHook(fn func(ctx context.Context, w Wiring) error) *TemporalDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *TemporalDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *TemporalDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
