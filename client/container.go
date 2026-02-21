package rig

import "context"

// ContainerDef defines a service backed by a Docker container. Use the
// Container() constructor for the common case.
type ContainerDef struct {
	image     string
	cmd       []string
	env       map[string]string
	ingresses map[string]IngressDef
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*ContainerDef) rigService() {}

// Container creates a container service definition with a default HTTP ingress.
// The container port must be set with .Port() or .Ingress().
//
//	rig.Container("nginx:alpine").Port(80)
//	rig.Container("myteam/api:latest").Port(3000)
func Container(image string) *ContainerDef {
	return &ContainerDef{
		image:     image,
		ingresses: map[string]IngressDef{"default": IngressHTTP()},
	}
}

// Port sets the container port for the default ingress. If ingresses were
// removed with NoIngress(), Port re-creates the default TCP ingress.
func (d *ContainerDef) Port(containerPort int) *ContainerDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	def := d.ingresses["default"]
	if def.Protocol == "" {
		def.Protocol = HTTP
	}
	def.ContainerPort = containerPort
	d.ingresses["default"] = def
	return d
}

// Cmd overrides the container's default command.
func (d *ContainerDef) Cmd(args ...string) *ContainerDef {
	d.cmd = args
	return d
}

// Env sets an environment variable on the container.
func (d *ContainerDef) Env(key, value string) *ContainerDef {
	if d.env == nil {
		d.env = make(map[string]string)
	}
	d.env[key] = value
	return d
}

// NoIngress removes all ingresses, for containers that are pure workers.
func (d *ContainerDef) NoIngress() *ContainerDef {
	d.ingresses = nil
	return d
}

// Ingress adds or overrides an ingress on the service.
func (d *ContainerDef) Ingress(name string, def IngressDef) *ContainerDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	d.ingresses[name] = def
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *ContainerDef) Egress(service string, ingress ...string) *ContainerDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *ContainerDef) EgressAs(name, service string, ingress ...string) *ContainerDef {
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
func (d *ContainerDef) InitHook(fn func(ctx context.Context, w Wiring) error) *ContainerDef {
	d.hooks.init = hookFunc(fn)
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *ContainerDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *ContainerDef {
	d.hooks.prestart = hookFunc(fn)
	return d
}
