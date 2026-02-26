package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/matgreaves/rig/internal/spec"
)

// BuildServiceEnv builds the full environment variable map for a service
// during its start phase. This includes:
//   - RIG_WIRING: full wiring as JSON for rig-aware services
//   - Service-level attributes (RIG_TEMP_DIR, RIG_ENV_DIR, etc.)
//   - Own ingress attributes (HOST/PORT for default, prefixed for named)
//   - Egress attributes (always prefixed by egress name)
//
// Rig-aware services should read RIG_WIRING. The flat env vars are a
// convenience fallback for services that don't know about rig.
func BuildServiceEnv(
	serviceName string,
	ingresses map[string]spec.Endpoint,
	egresses map[string]spec.Endpoint,
	tempDir string,
	envDir string,
) (map[string]string, error) {
	// Resolve attribute templates against each endpoint's Host/Port.
	// This is the output boundary — callers pass endpoints with templates
	// (possibly adjusted for their execution context, e.g. container networking)
	// and we produce concrete values.
	resolvedIngresses, err := resolveEndpointMap(ingresses)
	if err != nil {
		return nil, fmt.Errorf("resolve ingress attributes: %w", err)
	}
	resolvedEgresses, err := resolveEndpointMap(egresses)
	if err != nil {
		return nil, fmt.Errorf("resolve egress attributes: %w", err)
	}

	env := make(map[string]string)

	// RIG_WIRING: structured wiring as JSON. Preferred over flat env vars.
	wiring := WiringContext{
		Ingresses: resolvedIngresses,
		Egresses:  resolvedEgresses,
		TempDir:   tempDir,
		EnvDir:    envDir,
	}
	if b, err := json.Marshal(wiring); err == nil {
		env["RIG_WIRING"] = string(b)
	}

	// Flat env vars: fallback for services that don't read RIG_WIRING.
	env["RIG_TEMP_DIR"] = tempDir
	env["RIG_ENV_DIR"] = envDir
	env["RIG_SERVICE"] = serviceName

	// Ingress attributes: default ingress is unprefixed, named ingresses are prefixed.
	addIngressAttrs(env, resolvedIngresses)

	// Egress attributes: always prefixed by egress name.
	addEgressAttrs(env, resolvedEgresses)

	return env, nil
}

// BuildInitHookEnv builds the environment variable map for an init hook.
// Init hooks receive only the service's own ingress attributes — no egresses.
// Default ingress is unprefixed, named ingresses are prefixed.
func BuildInitHookEnv(
	serviceName string,
	ingresses map[string]spec.Endpoint,
	tempDir string,
	envDir string,
) (map[string]string, error) {
	// Resolve attribute templates at this output boundary.
	resolvedIngresses, err := resolveEndpointMap(ingresses)
	if err != nil {
		return nil, fmt.Errorf("resolve ingress attributes: %w", err)
	}

	env := make(map[string]string)

	// Service-level attributes.
	env["RIG_TEMP_DIR"] = tempDir
	env["RIG_ENV_DIR"] = envDir
	env["RIG_SERVICE"] = serviceName

	// Ingress attributes only — no egresses.
	addIngressAttrs(env, resolvedIngresses)

	return env, nil
}

// BuildPrestartHookEnv builds the environment variable map for a prestart hook.
// Prestart hooks receive full wiring: own ingresses + resolved egresses.
func BuildPrestartHookEnv(
	serviceName string,
	ingresses map[string]spec.Endpoint,
	egresses map[string]spec.Endpoint,
	tempDir string,
	envDir string,
) (map[string]string, error) {
	// Prestart hooks have the same env as the service itself.
	return BuildServiceEnv(serviceName, ingresses, egresses, tempDir, envDir)
}

// addIngressAttrs adds ingress attributes to the env map.
// If a "default" ingress exists, its attributes are unprefixed.
// All other ingresses have their attributes prefixed by the ingress name.
func addIngressAttrs(env map[string]string, ingresses map[string]spec.ResolvedEndpoint) {
	for name, ep := range ingresses {
		prefix := ""
		if name != "default" {
			prefix = toEnvPrefix(name)
		}
		addEndpointAttrs(env, prefix, ep)
	}
}

// addEgressAttrs adds egress attributes to the env map.
// Egresses are always prefixed by the egress name.
func addEgressAttrs(env map[string]string, egresses map[string]spec.ResolvedEndpoint) {
	for name, ep := range egresses {
		prefix := toEnvPrefix(name)
		addEndpointAttrs(env, prefix, ep)
	}
}

// addEndpointAttrs adds HOST, PORT, and all endpoint attributes to the env
// map with the given prefix. If prefix is empty, attributes are added without
// a prefix. Accepts ResolvedEndpoint to ensure templates have been expanded.
func addEndpointAttrs(env map[string]string, prefix string, ep spec.ResolvedEndpoint) {
	set := func(key, value string) {
		if prefix != "" {
			env[prefix+key] = value
		} else {
			env[key] = value
		}
	}

	host, portStr, _ := net.SplitHostPort(ep.HostPort)
	set("HOST", host)
	set("PORT", portStr)

	for k, v := range ep.Attributes {
		set(k, fmt.Sprintf("%v", v))
	}
}

// toEnvPrefix converts a name to an environment variable prefix.
// Hyphens are replaced with underscores and the result is uppercased
// with a trailing underscore. e.g. "order-db" → "ORDER_DB_".
func toEnvPrefix(name string) string {
	s := strings.ToUpper(name)
	s = strings.ReplaceAll(s, "-", "_")
	return s + "_"
}

// ExpandTemplates expands $VAR and ${VAR} references in a list of strings
// against the given attribute map.
func ExpandTemplates(templates []string, attrs map[string]string) []string {
	if len(templates) == 0 {
		return nil
	}
	result := make([]string, len(templates))
	for i, tmpl := range templates {
		result[i] = os.Expand(tmpl, func(key string) string {
			return attrs[key]
		})
	}
	return result
}

// ExpandTemplate expands $VAR and ${VAR} references in a single string.
func ExpandTemplate(tmpl string, attrs map[string]string) string {
	return os.Expand(tmpl, func(key string) string {
		return attrs[key]
	})
}
