package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func runDown(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rig down <environment-name-or-id>")
	}

	target := args[0]

	addr, err := serverAddr()
	if err != nil {
		return err
	}

	// Resolve target to an environment ID. If it looks like an ID (contains
	// no spaces and is short), try DELETE directly. Otherwise, list
	// environments and fuzzy-match by name.
	id, err := resolveEnvID(addr, target)
	if err != nil {
		return err
	}

	// Send DELETE.
	req, err := http.NewRequest(http.MethodDelete, addr+"/environments/"+id+"?log=true", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to rigd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("environment %q not found (may have already been torn down)", target)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rigd returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	fmt.Printf("Environment %s torn down.\n", result.ID)
	return nil
}

// resolveEnvID resolves a user-provided name or ID to an environment ID by
// querying the rigd list endpoint. Returns an error if no match is found.
func resolveEnvID(addr, target string) (string, error) {
	resp, err := http.Get(addr + "/environments")
	if err != nil {
		return "", fmt.Errorf("connect to rigd: %w", err)
	}
	defer resp.Body.Close()

	var entries []psEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", fmt.Errorf("decode environment list: %w", err)
	}

	// Exact ID match.
	for _, e := range entries {
		if e.ID == target {
			return e.ID, nil
		}
	}

	// Fuzzy name match: case-insensitive substring.
	var matches []psEntry
	lower := strings.ToLower(target)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), lower) {
			matches = append(matches, e)
		}
	}

	switch len(matches) {
	case 0:
		if len(entries) == 0 {
			return "", fmt.Errorf("no active environments")
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		return "", fmt.Errorf("no environment matching %q (active: %s)", target, strings.Join(names, ", "))
	case 1:
		return matches[0].ID, nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = fmt.Sprintf("%s (%s)", m.Name, m.ID)
		}
		return "", fmt.Errorf("ambiguous: %q matches %d environments: %s", target, len(matches), strings.Join(names, ", "))
	}
}
