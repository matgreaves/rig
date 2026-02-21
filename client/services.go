package rig

import "context"

// GoDef defines a service built from a Go module. Use the Go() constructor
// for the common case, or create a GoDef literal for full control.
type GoDef struct {
	module    string
	args      []string
	ingresses map[string]IngressDef
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*GoDef) rigService() {}

// Go creates a Go service definition with a default HTTP ingress.
// The module path is resolved relative to the working directory if not absolute.
//
// Chain methods to customize:
//
//	rig.Go("./cmd/api").
//	    Egress("postgres").
//	    InitHook(func(ctx context.Context, w rig.Wiring) error { ... })
func Go(module string) *GoDef {
	return &GoDef{
		module:    module,
		ingresses: map[string]IngressDef{"default": IngressHTTP()},
	}
}

// NoIngress removes all ingresses, for services that are pure workers
// with only egress dependencies.
func (d *GoDef) NoIngress() *GoDef {
	d.ingresses = nil
	return d
}

// Ingress adds or overrides an ingress on the service.
func (d *GoDef) Ingress(name string, def IngressDef) *GoDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	d.ingresses[name] = def
	return d
}

// Egress adds a dependency on a service. The egress is named after the
// target service. If ingress is provided, it specifies which ingress on the
// target; otherwise the target's sole ingress is used.
//
//	.Egress("postgres")           // egress "postgres" → postgres default
//	.Egress("postgres", "admin")  // egress "postgres" → postgres/admin
func (d *GoDef) Egress(service string, ingress ...string) *GoDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name. Use this when the
// egress name should differ from the target service name, e.g. when a
// service depends on the same target twice via different ingresses.
//
//	.EgressAs("db", "postgres")           // egress "db" → postgres default
//	.EgressAs("db", "postgres", "admin")  // egress "db" → postgres/admin
func (d *GoDef) EgressAs(name, service string, ingress ...string) *GoDef {
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

// Args sets command-line arguments (supports ${VAR} expansion).
func (d *GoDef) Args(args ...string) *GoDef {
	d.args = args
	return d
}

// InitHook registers a client-side function that runs after health checks
// pass, before the service is marked ready. Receives own ingresses only.
func (d *GoDef) InitHook(fn func(ctx context.Context, w Wiring) error) *GoDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side function that runs after egresses
// are resolved, before the service process starts. Receives full wiring.
func (d *GoDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *GoDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}

// FuncDef defines a service backed by a Go function running in the test
// process. The function receives a context with wiring injected — use
// connect.ParseWiring(ctx) to access it, just like a standalone binary.
type FuncDef struct {
	fn        func(ctx context.Context) error
	ingresses map[string]IngressDef
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*FuncDef) rigService() {}

// Func creates a service that runs fn in the test process. fn should behave
// like a service main: call connect.ParseWiring(ctx) to get its wiring, start
// serving, and block until ctx is cancelled.
//
// By default a single HTTP ingress is exposed. The same function can be used
// with rig.Go() if compiled into a binary — connect.ParseWiring reads from
// context when available, falling back to environment variables.
//
//	rig.Func(echo.Run).Egress("db")
func Func(fn func(ctx context.Context) error) *FuncDef {
	return &FuncDef{
		fn:        fn,
		ingresses: map[string]IngressDef{"default": IngressHTTP()},
	}
}

// NoIngress removes all ingresses.
func (d *FuncDef) NoIngress() *FuncDef {
	d.ingresses = nil
	return d
}

// Ingress adds or overrides an ingress on the service.
func (d *FuncDef) Ingress(name string, def IngressDef) *FuncDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	d.ingresses[name] = def
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *FuncDef) Egress(service string, ingress ...string) *FuncDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *FuncDef) EgressAs(name, service string, ingress ...string) *FuncDef {
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
func (d *FuncDef) InitHook(fn func(ctx context.Context, w Wiring) error) *FuncDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *FuncDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *FuncDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}

// ProcessDef defines a service that runs a pre-built binary. Use the
// Process() constructor or create a ProcessDef literal for full control.
type ProcessDef struct {
	command   string
	dir       string
	args      []string
	ingresses map[string]IngressDef
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*ProcessDef) rigService() {}

// Process creates a process service definition with a default HTTP ingress.
// The command must be the path to a pre-built binary.
//
//	rig.Process("/path/to/binary").
//	    Egress("postgres")
func Process(command string) *ProcessDef {
	return &ProcessDef{
		command:   command,
		ingresses: map[string]IngressDef{"default": IngressHTTP()},
	}
}

// NoIngress removes all ingresses, for services that are pure workers
// with only egress dependencies.
func (d *ProcessDef) NoIngress() *ProcessDef {
	d.ingresses = nil
	return d
}

// Dir sets the working directory for the process.
func (d *ProcessDef) Dir(dir string) *ProcessDef {
	d.dir = dir
	return d
}

// Ingress adds or overrides an ingress on the service.
func (d *ProcessDef) Ingress(name string, def IngressDef) *ProcessDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	d.ingresses[name] = def
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *ProcessDef) Egress(service string, ingress ...string) *ProcessDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *ProcessDef) EgressAs(name, service string, ingress ...string) *ProcessDef {
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

// Args sets command-line arguments (supports ${VAR} expansion).
func (d *ProcessDef) Args(args ...string) *ProcessDef {
	d.args = args
	return d
}

// InitHook registers a client-side init hook function.
func (d *ProcessDef) InitHook(fn func(ctx context.Context, w Wiring) error) *ProcessDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *ProcessDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *ProcessDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}

// CustomDef defines a service using any server-registered type. This is the
// escape hatch for types not yet modeled in the SDK.
type CustomDef struct {
	svcType   string
	config    map[string]any
	args      []string
	ingresses map[string]IngressDef
	egresses  map[string]egressDef
	hooks     hooksDef
}

func (*CustomDef) rigService() {}

// Custom creates a service definition for any server-registered type,
// with a default HTTP ingress.
func Custom(svcType string, config map[string]any) *CustomDef {
	return &CustomDef{
		svcType:   svcType,
		config:    config,
		ingresses: map[string]IngressDef{"default": IngressHTTP()},
	}
}

// NoIngress removes all ingresses, for services that are pure workers
// with only egress dependencies.
func (d *CustomDef) NoIngress() *CustomDef {
	d.ingresses = nil
	return d
}

// Ingress adds or overrides an ingress on the service.
func (d *CustomDef) Ingress(name string, def IngressDef) *CustomDef {
	if d.ingresses == nil {
		d.ingresses = make(map[string]IngressDef)
	}
	d.ingresses[name] = def
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *CustomDef) Egress(service string, ingress ...string) *CustomDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *CustomDef) EgressAs(name, service string, ingress ...string) *CustomDef {
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

// Args sets command-line arguments.
func (d *CustomDef) Args(args ...string) *CustomDef {
	d.args = args
	return d
}

// InitHook registers a client-side init hook function.
func (d *CustomDef) InitHook(fn func(ctx context.Context, w Wiring) error) *CustomDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *CustomDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *CustomDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
