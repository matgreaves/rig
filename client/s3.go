package rig

import "context"

// S3Def defines a service backed by the builtin S3 type.
// Rig manages the container lifecycle and bucket isolation — the API
// is minimal.
//
// Publishes S3_ENDPOINT, S3_BUCKET, AWS_ACCESS_KEY_ID, and
// AWS_SECRET_ACCESS_KEY as endpoint attributes.
// Each environment gets an isolated bucket assigned by the server.
type S3Def struct {
	egresses map[string]egressDef
	hooks    hooksDef
}

func (*S3Def) rigService() {}

// S3 creates an S3 service definition backed by MinIO.
// Each environment gets an isolated bucket assigned automatically by the
// server.
//
//	rig.S3()
func S3() *S3Def {
	return &S3Def{}
}

// Egress adds a dependency on a service, named after the target.
func (d *S3Def) Egress(service string) *S3Def {
	return d.EgressAs(service, service)
}

// EgressAs adds a dependency with a custom local name.
func (d *S3Def) EgressAs(name, service string, ingress ...string) *S3Def {
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
func (d *S3Def) InitHook(fn func(ctx context.Context, w Wiring) error) *S3Def {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *S3Def) PrestartHook(fn func(ctx context.Context, w Wiring) error) *S3Def {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
