package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeEnvironment unmarshals an environment spec from JSON, detecting
// duplicate keys that encoding/json would silently ignore.
func DecodeEnvironment(data []byte) (Environment, error) {
	// First, check for duplicate service names.
	var raw struct {
		Name     string                      `json:"name"`
		Services map[string]json.RawMessage  `json:"services"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Environment{}, err
	}

	if err := checkDuplicateKeys(data, "services"); err != nil {
		return Environment{}, err
	}

	// Now unmarshal each service and check for duplicate ingress/egress keys.
	env := Environment{
		Name:     raw.Name,
		Services: make(map[string]Service, len(raw.Services)),
	}

	for svcName, svcData := range raw.Services {
		if err := checkDuplicateKeys(svcData, "ingresses"); err != nil {
			return Environment{}, fmt.Errorf("service %q: %w", svcName, err)
		}
		if err := checkDuplicateKeys(svcData, "egresses"); err != nil {
			return Environment{}, fmt.Errorf("service %q: %w", svcName, err)
		}

		var svc Service
		if err := json.Unmarshal(svcData, &svc); err != nil {
			return Environment{}, fmt.Errorf("service %q: %w", svcName, err)
		}
		env.Services[svcName] = svc
	}

	return env, nil
}

// checkDuplicateKeys checks whether a JSON object at the given field name
// contains duplicate keys. Returns an error if duplicates are found.
func checkDuplicateKeys(data []byte, field string) error {
	// Parse the outer object to find the field value.
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil // not a JSON object or parse error — let standard unmarshal handle it
	}

	fieldData, ok := outer[field]
	if !ok {
		return nil // field not present
	}

	// Use json.Decoder to walk tokens and detect duplicate keys.
	dec := json.NewDecoder(bytes.NewReader(fieldData))
	return checkObjectDuplicates(dec, field)
}

func checkObjectDuplicates(dec *json.Decoder, context string) error {
	// Read opening brace.
	t, err := dec.Token()
	if err != nil {
		return nil
	}
	delim, ok := t.(json.Delim)
	if !ok || delim != '{' {
		return nil // not an object
	}

	seen := make(map[string]bool)
	for dec.More() {
		// Read key.
		t, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := t.(string)
		if !ok {
			return nil
		}
		if seen[key] {
			return fmt.Errorf("duplicate %s key: %q", context, key)
		}
		seen[key] = true

		// Skip the value (could be any JSON value including nested objects/arrays).
		var discard json.RawMessage
		if err := dec.Decode(&discard); err != nil {
			return nil
		}
	}

	return nil
}

