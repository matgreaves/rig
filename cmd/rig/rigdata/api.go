package rigdata

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ServerAddr reads the rigd server address from the addr file on disk.
// The version parameter is used to locate the correct addr file.
// It tries the versioned file first, then falls back to unversioned (legacy).
func ServerAddr(version string) (string, error) {
	rigDir := DefaultRigDir()

	candidates := []string{
		filepath.Join(rigDir, "rigd-v"+version+".addr"),
		filepath.Join(rigDir, "rigd.addr"),
	}

	for _, addrFile := range candidates {
		data, err := os.ReadFile(addrFile)
		if err != nil {
			continue
		}
		addr := strings.TrimSpace(string(data))
		if addr == "" {
			continue
		}
		return "http://" + addr, nil
	}
	return "", fmt.Errorf("rigd is not running (no addr file in %s)", rigDir)
}

// FetchEnvironments fetches the list of active environments from the server.
func FetchEnvironments(addr string) ([]PsEntry, error) {
	resp, err := http.Get(addr + "/environments")
	if err != nil {
		return nil, fmt.Errorf("connect to rigd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rigd returned %d: %s", resp.StatusCode, body)
	}

	var entries []PsEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return entries, nil
}

// FetchResolved fetches the fully resolved state of an environment.
func FetchResolved(addr, id string) (*ResolvedEnv, error) {
	resp, err := http.Get(addr + "/environments/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env ResolvedEnv
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

// ResolveEnvID resolves a target (name or ID) to an environment ID.
// It fetches the list of active environments and does fuzzy matching.
func ResolveEnvID(addr, target string) (string, error) {
	entries, err := FetchEnvironments(addr)
	if err != nil {
		return "", err
	}

	// Exact ID match.
	for _, e := range entries {
		if e.ID == target {
			return e.ID, nil
		}
	}

	// Fuzzy name match (case-insensitive substring).
	var matches []PsEntry
	lower := strings.ToLower(target)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), lower) ||
			strings.Contains(strings.ToLower(e.ID), lower) {
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
			names[i] = fmt.Sprintf("  %s  %s", e.Name, e.ID)
		}
		return "", fmt.Errorf("no environment matching %q\n\nActive environments:\n%s",
			target, strings.Join(names, "\n"))
	case 1:
		return matches[0].ID, nil
	default:
		names := make([]string, len(matches))
		for i, e := range matches {
			names[i] = fmt.Sprintf("  %s  %s", e.Name, e.ID)
		}
		return "", fmt.Errorf("ambiguous match %q — matches %d environments:\n%s",
			target, len(matches), strings.Join(names, "\n"))
	}
}

// TearDown sends a DELETE request to tear down an environment.
func TearDown(addr, id string) error {
	req, err := http.NewRequest(http.MethodDelete, addr+"/environments/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// ConnectionURL builds a canonical, clickable connection URL from the
// endpoint's protocol and attributes.
func ConnectionURL(ep ResolvedEP) string {
	a := ep.Attributes

	if pgHost, ok := a["PGHOST"]; ok {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
			AttrStr(a, "PGUSER"), AttrStr(a, "PGPASSWORD"),
			pgHost, AttrStr(a, "PGPORT"), AttrStr(a, "PGDATABASE"))
	}

	if redisURL, ok := a["REDIS_URL"]; ok {
		return fmt.Sprintf("%v", redisURL)
	}

	if addr, ok := a["TEMPORAL_ADDRESS"]; ok {
		ns := AttrStr(a, "TEMPORAL_NAMESPACE")
		if ns != "" {
			return fmt.Sprintf("%v  namespace=%s", addr, ns)
		}
		return fmt.Sprintf("%v", addr)
	}

	if s3ep, ok := a["S3_ENDPOINT"]; ok {
		parts := []string{fmt.Sprintf("%v", s3ep)}
		if bucket := AttrStr(a, "S3_BUCKET"); bucket != "" {
			parts = append(parts, "bucket="+bucket)
		}
		if user := AttrStr(a, "AWS_ACCESS_KEY_ID"); user != "" {
			parts = append(parts, "user="+user)
			if pass := AttrStr(a, "AWS_SECRET_ACCESS_KEY"); pass != "" {
				parts = append(parts, "pass="+pass)
			}
		}
		return strings.Join(parts, "  ")
	}

	if queueURL, ok := a["SQS_QUEUE_URL"]; ok {
		parts := []string{fmt.Sprintf("%v", queueURL)}
		if user := AttrStr(a, "AWS_ACCESS_KEY_ID"); user != "" {
			parts = append(parts, "user="+user)
		}
		return strings.Join(parts, "  ")
	}

	if ep.Protocol == "http" {
		return "http://" + ep.HostPort
	}

	return ep.HostPort
}

// AttrStr returns the string value of an attribute, or "" if missing.
func AttrStr(attrs map[string]any, key string) string {
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
