package rig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// hookSeq generates unique hook names across the process.
var hookSeq atomic.Uint64

// envToSpec converts the SDK Services to the specEnvironment wire format.
// Hook functions are registered in handlers keyed by their generated name.
// Start functions (from FuncDef) are registered in startHandlers.
func envToSpec(testName string, services Services, handlers map[string]hookFunc, startHandlers map[string]startFunc, observe bool) (specEnvironment, error) {
	specs := make(map[string]specService, len(services))
	for name, def := range services {
		svc, err := serviceToSpec(def, handlers, startHandlers)
		if err != nil {
			return specEnvironment{}, fmt.Errorf("service %q: %w", name, err)
		}
		specs[name] = svc
	}
	return specEnvironment{
		Name:     testName,
		Services: specs,
		Observe:  observe,
	}, nil
}

func serviceToSpec(def ServiceDef, handlers map[string]hookFunc, startHandlers map[string]startFunc) (specService, error) {
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
		return specService{}, fmt.Errorf("unknown service type: %T", def)
	}
}

func goToSpec(d *GoDef, handlers map[string]hookFunc) (specService, error) {
	module := d.module
	if !filepath.IsAbs(module) {
		wd, err := os.Getwd()
		if err != nil {
			return specService{}, fmt.Errorf("resolve module path: %w", err)
		}
		module = filepath.Join(wd, module)
	}

	cfg, _ := json.Marshal(map[string]string{"module": module})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:      "go",
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func processToSpec(d *ProcessDef, handlers map[string]hookFunc) (specService, error) {
	cfg, _ := json.Marshal(map[string]string{"command": d.command, "dir": d.dir})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:      "process",
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func funcToSpec(d *FuncDef, handlers map[string]hookFunc, startHandlers map[string]startFunc) (specService, error) {
	name := fmt.Sprintf("_start_%d", hookSeq.Add(1))
	startHandlers[name] = startFunc(d.fn)

	cfg, _ := json.Marshal(map[string]string{"start_handler": name})

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:      "client",
		Config:    cfg,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func postgresToSpec(d *PostgresDef, handlers map[string]hookFunc) (specService, error) {
	var cfg json.RawMessage
	if d.image != "" {
		cfg, _ = json.Marshal(map[string]string{"image": d.image})
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:   "postgres",
		Config: cfg,
		Ingresses: map[string]specIngressSpec{
			"default": {Protocol: TCP, ContainerPort: 5432},
		},
		Egresses: egressesToSpec(d.egresses),
		Hooks:    hooks,
	}, nil
}

func containerToSpec(d *ContainerDef, handlers map[string]hookFunc) (specService, error) {
	cfgMap := map[string]any{"image": d.image}
	if len(d.cmd) > 0 {
		cfgMap["cmd"] = d.cmd
	}
	if len(d.env) > 0 {
		cfgMap["env"] = d.env
	}
	cfg, err := json.Marshal(cfgMap)
	if err != nil {
		return specService{}, fmt.Errorf("marshal container config: %w", err)
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:      "container",
		Config:    cfg,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func customToSpec(d *CustomDef, handlers map[string]hookFunc) (specService, error) {
	var cfg json.RawMessage
	if d.config != nil {
		var err error
		cfg, err = json.Marshal(d.config)
		if err != nil {
			return specService{}, fmt.Errorf("marshal custom config: %w", err)
		}
	}

	hooks, err := hooksToSpec(d.hooks, handlers)
	if err != nil {
		return specService{}, err
	}

	return specService{
		Type:      d.svcType,
		Config:    cfg,
		Args:      d.args,
		Ingresses: ingressesToSpec(d.ingresses),
		Egresses:  egressesToSpec(d.egresses),
		Hooks:     hooks,
	}, nil
}

func ingressesToSpec(ingresses map[string]IngressDef) map[string]specIngressSpec {
	if len(ingresses) == 0 {
		return nil
	}
	out := make(map[string]specIngressSpec, len(ingresses))
	for name, ing := range ingresses {
		s := specIngressSpec{
			Protocol:      Protocol(ing.Protocol),
			ContainerPort: ing.ContainerPort,
			Attributes:    ing.Attributes,
		}
		if ing.Ready != nil {
			s.Ready = &specReadySpec{
				Type: ing.Ready.Type,
				Path: ing.Ready.Path,
			}
			if ing.Ready.Interval > 0 {
				s.Ready.Interval = specDuration{Duration: ing.Ready.Interval}
			}
			if ing.Ready.Timeout > 0 {
				s.Ready.Timeout = specDuration{Duration: ing.Ready.Timeout}
			}
		}
		out[name] = s
	}
	return out
}

func egressesToSpec(egresses map[string]egressDef) map[string]specEgressSpec {
	if len(egresses) == 0 {
		return nil
	}
	out := make(map[string]specEgressSpec, len(egresses))
	for name, eg := range egresses {
		out[name] = specEgressSpec{
			Service: eg.service,
			Ingress: eg.ingress,
		}
	}
	return out
}

func hooksToSpec(h hooksDef, handlers map[string]hookFunc) (*specHooks, error) {
	if len(h.prestart) == 0 && len(h.init) == 0 {
		return nil, nil
	}

	var hooks specHooks

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

func hookToSpec(h hook, handlers map[string]hookFunc) (*specHookSpec, error) {
	switch hk := h.(type) {
	case hookFunc:
		name := fmt.Sprintf("_hook_%d", hookSeq.Add(1))
		handlers[name] = hk
		return &specHookSpec{
			Type:       "client_func",
			ClientFunc: &specClientFuncSpec{Name: name},
		}, nil
	case sqlHook:
		cfg, _ := json.Marshal(map[string]any{"statements": hk.statements})
		return &specHookSpec{
			Type:   "sql",
			Config: cfg,
		}, nil
	case execHook:
		cfg, _ := json.Marshal(map[string]any{"command": hk.command})
		return &specHookSpec{
			Type:   "exec",
			Config: cfg,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported hook type: %T", h)
	}
}

func temporalToSpec(d *TemporalDef, handlers map[string]hookFunc) (specService, error) {
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
		return specService{}, err
	}

	return specService{
		Type:   "temporal",
		Config: cfg,
		Ingresses: map[string]specIngressSpec{
			"default": {Protocol: GRPC},
			"ui":      {Protocol: HTTP},
		},
		Egresses: egressesToSpec(d.egresses),
		Hooks:    hooks,
	}, nil
}
