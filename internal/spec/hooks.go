package spec

import "encoding/json"

// HookSpec defines a lifecycle hook (prestart or init).
type HookSpec struct {
	// Type identifies the hook implementation:
	//   "client_func" — callback to client-side function
	//   "script"      — run a shell command
	//   builtin names — service-type-specific (e.g. "initdb", "create-namespace")
	Type string `json:"type"`

	// ClientFunc holds config for client_func hooks.
	ClientFunc *ClientFuncSpec `json:"client_func,omitempty"`

	// Config holds type-specific configuration as raw JSON.
	// Interpretation depends on the hook type.
	Config json.RawMessage `json:"config,omitempty"`
}

// ClientFuncSpec identifies a client-side function to call back to.
type ClientFuncSpec struct {
	// Name is the key used to look up the handler in the SDK's registry.
	Name string `json:"name"`
}
