package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/matgreaves/rig/internal/spec"
)

// KnownServiceTypes is the set of service types built into rigd.
// Custom client-side types are declared with the "custom" type.
var KnownServiceTypes = map[string]bool{
	"container": true,
	"process":   true,
	"script":    true,
	"go":        true,
	"client":    true,
	"postgres":  true,
	"temporal":  true,
	"redis":     true,
	"custom":    true,
	"proxy":     true,
	"test":      true,
}

// ValidateEnvironment checks an environment spec for structural errors.
// It calls ResolveDefaults first to fill in default values, then validates.
// Returns all errors found (not just the first) so the user can fix them
// in one pass.
func ValidateEnvironment(env *spec.Environment) []string {
	ResolveDefaults(env)

	var errs []string

	if env.Name == "" {
		errs = append(errs, "environment name is required")
	}

	if len(env.Services) == 0 {
		errs = append(errs, "environment must have at least one service")
	}

	// Sort service names for deterministic error ordering.
	names := sortedKeys(env.Services)

	for _, name := range names {
		svc := env.Services[name]
		errs = append(errs, validateService(name, svc, env.Services)...)
	}

	if cycle := detectCycle(env.Services); cycle != "" {
		errs = append(errs, cycle)
	}

	return errs
}

func sortedKeys(services map[string]spec.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateService(name string, svc spec.Service, allServices map[string]spec.Service) []string {
	var errs []string

	// Service type must be known.
	if svc.Type == "" {
		errs = append(errs, fmt.Sprintf("service %q: type is required", name))
	} else if !KnownServiceTypes[svc.Type] {
		errs = append(errs, fmt.Sprintf("service %q: unknown type %q", name, svc.Type))
	}

	// Validate ingresses (sorted for deterministic output).
	for _, ingressName := range ingressNames(svc.Ingresses) {
		ingress := svc.Ingresses[ingressName]

		if !ingress.Protocol.Valid() {
			errs = append(errs, fmt.Sprintf(
				"service %q, ingress %q: invalid protocol %q (must be one of: tcp, http, grpc, kafka)",
				name, ingressName, ingress.Protocol,
			))
		}

		// ContainerPort is optional for container types: if omitted, the
		// host-allocated port is used as the container port (rig-native
		// apps that read RIG_DEFAULT_PORT).
	}

	// Validate egresses (sorted for deterministic output).
	egressNames := make([]string, 0, len(svc.Egresses))
	for n := range svc.Egresses {
		egressNames = append(egressNames, n)
	}
	sort.Strings(egressNames)

	for _, egressName := range egressNames {
		egress := svc.Egresses[egressName]

		// Self-reference.
		if egress.Service == name {
			errs = append(errs, fmt.Sprintf(
				"service %q, egress %q: cannot reference itself",
				name, egressName,
			))
			continue
		}

		// Target service must exist.
		target, ok := allServices[egress.Service]
		if !ok {
			msg := fmt.Sprintf(
				"service %q, egress %q: references unknown service %q",
				name, egressName, egress.Service,
			)
			if suggestion := closestMatch(egress.Service, allServices); suggestion != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", suggestion)
			}
			errs = append(errs, msg)
			continue
		}

		// Target must have at least one ingress for an egress to reference it.
		if len(target.Ingresses) == 0 {
			errs = append(errs, fmt.Sprintf(
				"service %q, egress %q: target service %q has no ingresses",
				name, egressName, egress.Service,
			))
			continue
		}

		if egress.Ingress != "" {
			// Explicit ingress name — must exist on target.
			if _, ok := target.Ingresses[egress.Ingress]; !ok {
				available := ingressNames(target.Ingresses)
				errs = append(errs, fmt.Sprintf(
					"service %q, egress %q: target service %q has no ingress %q (available: %s)",
					name, egressName, egress.Service, egress.Ingress, strings.Join(available, ", "),
				))
			}
		} else {
			// ResolveDefaults would have resolved this if the target had
			// exactly one ingress or one named "default". If we're still
			// here with an empty Ingress, it's genuinely ambiguous.
			if len(target.Ingresses) > 1 {
				available := ingressNames(target.Ingresses)
				errs = append(errs, fmt.Sprintf(
					"service %q, egress %q: target service %q has %d ingresses — specify which one (%s)",
					name, egressName, egress.Service, len(target.Ingresses), strings.Join(available, ", "),
				))
			}
		}
	}

	return errs
}

// ResolveDefaults fills in default values on the environment spec.
// Called automatically by ValidateEnvironment.
func ResolveDefaults(env *spec.Environment) {
	// Resolve egress ingress shorthand: if the egress doesn't specify
	// which ingress to target, auto-resolve it. First try single-ingress
	// shorthand (target has exactly one), then fall back to "default".
	for name, svc := range env.Services {
		for egressName, egress := range svc.Egresses {
			if egress.Ingress == "" {
				if target, ok := env.Services[egress.Service]; ok {
					if len(target.Ingresses) == 1 {
						for ingressName := range target.Ingresses {
							egress.Ingress = ingressName
						}
						svc.Egresses[egressName] = egress
					} else if _, hasDefault := target.Ingresses["default"]; hasDefault {
						egress.Ingress = "default"
						svc.Egresses[egressName] = egress
					}
				}
			}
		}
		env.Services[name] = svc
	}
}

// detectCycle walks the egress dependency graph using DFS and returns a
// descriptive error if a cycle is found. Returns "" if the graph is acyclic.
func detectCycle(services map[string]spec.Service) string {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int, len(services))
	parent := make(map[string]string, len(services))

	// Sort service names for deterministic output.
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	var dfs func(name string) string
	dfs = func(name string) string {
		state[name] = visiting

		svc := services[name]

		// Sort egress names for deterministic cycle path output.
		egressOrder := make([]string, 0, len(svc.Egresses))
		for n := range svc.Egresses {
			egressOrder = append(egressOrder, n)
		}
		sort.Strings(egressOrder)

		for _, eName := range egressOrder {
			target := svc.Egresses[eName].Service
			if _, ok := services[target]; !ok {
				continue // broken ref — caught by validateService
			}

			switch state[target] {
			case visiting:
				// Found a cycle — build the path.
				path := []string{target, name}
				for cur := name; cur != target; {
					cur = parent[cur]
					path = append(path, cur)
				}
				// Reverse to get forward order.
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return fmt.Sprintf("cycle detected: %s", strings.Join(path, " → "))
			case unvisited:
				parent[target] = name
				if msg := dfs(target); msg != "" {
					return msg
				}
			}
		}

		state[name] = visited
		return ""
	}

	for _, name := range names {
		if state[name] == unvisited {
			if msg := dfs(name); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// closestMatch returns the service name closest to target using simple
// edit distance, or "" if no name is close enough.
func closestMatch(target string, services map[string]spec.Service) string {
	best := ""
	bestDist := len(target)/2 + 1 // threshold: must be within half the length

	for name := range services {
		d := editDistance(target, name)
		if d < bestDist {
			bestDist = d
			best = name
		}
	}
	return best
}

func editDistance(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

func ingressNames(ingresses map[string]spec.IngressSpec) []string {
	names := make([]string, 0, len(ingresses))
	for name := range ingresses {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
