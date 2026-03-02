package rig

import "context"

// RedisDef defines a service backed by the builtin Redis type.
// Rig manages the container lifecycle and database isolation — the API
// is minimal.
//
// Publishes REDIS_URL as an endpoint attribute.
// Each environment gets an isolated database assigned by the server.
type RedisDef struct {
	image    string
	egresses map[string]egressDef
	hooks    hooksDef
}

func (*RedisDef) rigService() {}

// Redis creates a Redis service definition. By default uses redis:7-alpine.
// Each environment gets an isolated database assigned automatically by the
// server.
//
//	rig.Redis()
//	rig.Redis().Image("redis:6-alpine")
func Redis() *RedisDef {
	return &RedisDef{}
}

// Image overrides the default Redis Docker image (redis:7-alpine).
func (d *RedisDef) Image(image string) *RedisDef {
	d.image = image
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *RedisDef) Egress(service string, ingress ...string) *RedisDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *RedisDef) EgressAs(name, service string, ingress ...string) *RedisDef {
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
func (d *RedisDef) InitHook(fn func(ctx context.Context, w Wiring) error) *RedisDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *RedisDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *RedisDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
