package rig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/matgreaves/rig/spec"
)

// hookSeq generates unique hook names across the process.
var hookSeq atomic.Uint64

// envToSpec converts the SDK Services to the spec.Environment wire format.
// Hook functions are registered in handlers keyed by their generated name.
// Start functions (from FuncDef) are registered in startHandlers.
func envToSpec(testName string, services Services, handlers map[string]hookFunc, startHandlers map[string]startFunc, observe bool) (spec.Environment, error) {
	specs := make(map[string]spec.Service, len(services))
	for name, def := range services {
		svc, err := serviceToSpec(def, handlers, startHandlers)
		if err != nil {
			return spec.Environment{}, fmt.Errorf("service %q: %w", name, err)
		}
		specs[name] = svc
	}
	return spec.Environment{
		Name:     testName,
		Services: specs,
		Observe:  observe,
	}, nil
}

func serviceToSpec(def ServiceDef, handlers map[string]hookFunc, startHandlers map[string]startFunc) (spec.Service, error) {
	switch d := def.(type) {
	case *GoDef:
		return goToSpec(d, handlers)
	case *ProcessDef:
		return processToSpec(d, handlers)
	case *FuncDef:
		return funcToSpec(d, handlers, startHandlers)
	case *ContainerDef:
		return containerToSpec(d, handlers)
	case *PostgresDef:
		return postgresToSpec(d, handlers)
	case *CustomDef:
		return customToSpec(d, handlers)
	case *TemporalDef:
		return temporalToSpec(d, handlers)
	default:
		return spec.Service{}, fmt.Errorf("unknown service type: %T", def)
	}
}

func goToSpec(d *GoDef, handlers map[string]hookFunc) (spec.Service, error) {
	module := d.module
	if !filepath.IsAbs(module) {
		wd, err := os.Getwd()
		if err != nil {
			return spec.Service{}, fmt.Errorf("resolve module path: %w", err)
		}
		module = filepath.Join(wd, module)
	}

	cfg, _ := json.Marshal(map[string]string{"module": module})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:      "go",
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func processToSpec(d *ProcessDef, handlers map[string]hookFunc) (spec.Service, error) {
	cfg, _ := json.Marshal(map[string]string{"command": d.command, "dir": d.dir})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:      "process",
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func funcToSpec(d *FuncDef, handlers map[string]hookFunc, startHandlers map[string]startFunc) (spec.Service, error) {
	name := fmt.Sprintf("_start_%d", hookSeq.Add(1))
	startHandlers[name] = startFunc(d.fn)

	cfg, _ := json.Marshal(map[string]string{"start_handler": name})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:      "client",
		Config:    cfg,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func postgresToSpec(d *PostgresDef, handlers map[string]hookFunc) (spec.Service, error) {
	var cfg json.RawMessage
	if d.image != "" {
		cfg, _ = json.Marshal(map[string]string{"image": d.image})
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:   "postgres",
		Config: cfg,
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.TCP, ContainerPort: 5432},
		},
		Egresses: egressesToSpec(d.egresses),
		Hooks:    hooks,
	}, nil
}

func containerToSpec(d *ContainerDef, handlers map[string]hookFunc) (spec.Service, error) {
	cfgMap := map[string]any{"image": d.image}
	if len(d.cmd) > 0 {
		cfgMap["cmd"] = d.cmd
	}
	if len(d.env) > 0 {
		cfgMap["env"] = d.env
	}
	cfg, err := json.Marshal(cfgMap)
	if err != nil {
		return spec.Service{}, fmt.Errorf("marshal container config: %w", err)
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:      "container",
		Config:    cfg,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func customToSpec(d *CustomDef, handlers map[string]hookFunc) (spec.Service, error) {
	var cfg json.RawMessage
	if d.config != nil {
		var err error
		cfg, err = json.Marshal(d.config)
		if err != nil {
			return spec.Service{}, fmt.Errorf("marshal custom config: %w", err)
		}
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:      d.svcType,
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func ingressesToSpec(ingresses map[string]IngressDef) map[string]spec.IngressSpec {
	if len(ingresses) == 0 {
		return nil
	}
	out := make(map[string]spec.IngressSpec, len(ingresses))
	for name, ing := range ingresses {
		s := spec.IngressSpec{
			Protocol:      spec.Protocol(ing.Protocol),
			ContainerPort: ing.ContainerPort,
			Attributes:    ing.Attributes,
		}
		if ing.Ready != nil {
			s.Ready = &spec.ReadySpec{
				Type: ing.Ready.Type,
				Path: ing.Ready.Path,
			}
			if ing.Ready.Interval > 0 {
				s.Ready.Interval = spec.Duration{Duration: ing.Ready.Interval}
			}
			if ing.Ready.Timeout > 0 {
				s.Ready.Timeout = spec.Duration{Duration: ing.Ready.Timeout}
			}
		}
		out[name] = s
	}
	return out
}

func egressesToSpec(egresses map[string]egressDef) map[string]spec.EgressSpec {
	if len(egresses) == 0 {
		return nil
	}
	out := make(map[string]spec.EgressSpec, len(egresses))
	for name, eg := range egresses {
		out[name] = spec.EgressSpec{
			Service: eg.service,
			Ingress: eg.ingress,
		}
	}
	return out
}

func hooksToSpec(h hooksDef, handlers map[string]hookFunc) (*spec.Hooks, error) {
	if len(h.prestart) == 0 && len(h.init) == 0 {
		return nil, nil
	}

	var hooks spec.Hooks

	for _, hk := range h.prestart {
		hs, err := hookToSpec(hk, handlers)
		if err != nil {
			return nil, fmt.Errorf("prestart: %w", err)
		}
		hooks.Prestart = append(hooks.Prestart, hs)
	}

	for _, hk := range h.init {
		hs, err := hookToSpec(hk, handlers)
		if err != nil {
			return nil, fmt.Errorf("init: %w", err)
		}
		hooks.Init = append(hooks.Init, hs)
	}

	return &hooks, nil
}

func hookToSpec(h hook, handlers map[string]hookFunc) (*spec.HookSpec, error) {
	switch hk := h.(type) {
	case hookFunc:
		name := fmt.Sprintf("_hook_%d", hookSeq.Add(1))
		handlers[name] = hk
		return &spec.HookSpec{
			Type:       "client_func",
			ClientFunc: &spec.ClientFuncSpec{Name: name},
		}, nil
	case sqlHook:
		cfg, _ := json.Marshal(map[string]any{"statements": hk.statements})
		return &spec.HookSpec{
			Type:   "sql",
			Config: cfg,
		}, nil
	case execHook:
		cfg, _ := json.Marshal(map[string]any{"command": hk.command})
		return &spec.HookSpec{
			Type:   "exec",
			Config: cfg,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported hook type: %T", h)
	}
}

func temporalToSpec(d *TemporalDef, handlers map[string]hookFunc) (spec.Service, error) {
	var cfg json.RawMessage
	if d.version != "" || d.namespace != "" {
		cfgMap := make(map[string]string)
		if d.version != "" {
			cfgMap["version"] = d.version
		}
		if d.namespace != "" {
			cfgMap["namespace"] = d.namespace
		}
		cfg, _ = json.Marshal(cfgMap)
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return spec.Service{}, err
	}

	return spec.Service{
		Type:   "temporal",
		Config: cfg,
		Ingresses: map[string]spec.IngressSpec{
			"default": {Protocol: spec.GRPC},
			"ui":      {Protocol: spec.HTTP},
		},
		Egresses: egressesToSpec(d.egresses),
		Hooks:    hooks,
	}, nil
}
