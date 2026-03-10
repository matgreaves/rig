package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

type psEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	TTL          string   `json:"ttl,omitempty"`
	RemainingTTL string   `json:"remaining_ttl"`
	Services     []string `json:"services"`
}

type resolvedEnv struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Services map[string]resolvedSvc `json:"services"`
}

type resolvedSvc struct {
	Ingresses map[string]resolvedEP `json:"ingresses"`
	Status    string                `json:"status"`
}

type resolvedEP struct {
	HostPort   string         `json:"hostport"`
	Protocol   string         `json:"protocol"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

func runPs(args []string) error {
	addr, err := serverAddr()
	if err != nil {
		return err
	}

	resp, err := http.Get(addr + "/environments")
	if err != nil {
		return fmt.Errorf("connect to rigd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rigd returned %d: %s", resp.StatusCode, body)
	}

	var entries []psEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No active environments.")
		return nil
	}

	for i, e := range entries {
		resolved, err := fetchResolved(addr, e.ID)
		if err != nil {
			continue
		}
		if i > 0 {
			fmt.Println()
		}
		renderEnvironment(e, resolved)
	}

	return nil
}

func fetchResolved(addr, id string) (*resolvedEnv, error) {
	resp, err := http.Get(addr + "/environments/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env resolvedEnv
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

func renderEnvironment(entry psEntry, env *resolvedEnv) {
	fmt.Printf("%s  %s  expires in %s\n", bold(entry.Name), dim(entry.ID), entry.RemainingTTL)

	svcNames := make([]string, 0, len(env.Services))
	for name := range env.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)

	for _, svcName := range svcNames {
		svc := env.Services[svcName]

		ingNames := make([]string, 0, len(svc.Ingresses))
		for name := range svc.Ingresses {
			ingNames = append(ingNames, name)
		}
		sort.Strings(ingNames)

		for _, ingName := range ingNames {
			ep := svc.Ingresses[ingName]

			label := svcName
			if ingName != "default" {
				label = svcName + "/" + ingName
			}

			url := connectionURL(ep)
			fmt.Printf("  %-20s  %s\n", label, url)
		}
	}
}

// connectionURL builds a canonical, clickable connection URL from the
// endpoint's protocol and attributes.
func connectionURL(ep resolvedEP) string {
	a := ep.Attributes

	// Postgres: build a psql-compatible connection string.
	if pgHost, ok := a["PGHOST"]; ok {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
			attrStr(a, "PGUSER"), attrStr(a, "PGPASSWORD"),
			pgHost, attrStr(a, "PGPORT"), attrStr(a, "PGDATABASE"))
	}

	// Redis: use the REDIS_URL attribute directly.
	if redisURL, ok := a["REDIS_URL"]; ok {
		return fmt.Sprintf("%v", redisURL)
	}

	// Temporal: show the address and namespace.
	if addr, ok := a["TEMPORAL_ADDRESS"]; ok {
		ns := attrStr(a, "TEMPORAL_NAMESPACE")
		if ns != "" {
			return fmt.Sprintf("%v  namespace=%s", addr, ns)
		}
		return fmt.Sprintf("%v", addr)
	}

	// S3: show the endpoint, bucket, and credentials (needed for MinIO console login).
	if s3ep, ok := a["S3_ENDPOINT"]; ok {
		parts := []string{fmt.Sprintf("%v", s3ep)}
		if bucket := attrStr(a, "S3_BUCKET"); bucket != "" {
			parts = append(parts, "bucket="+bucket)
		}
		if user := attrStr(a, "AWS_ACCESS_KEY_ID"); user != "" {
			parts = append(parts, "user="+user)
			if pass := attrStr(a, "AWS_SECRET_ACCESS_KEY"); pass != "" {
				parts = append(parts, "pass="+pass)
			}
		}
		return strings.Join(parts, "  ")
	}

	// SQS: show the queue URL and credentials.
	if queueURL, ok := a["SQS_QUEUE_URL"]; ok {
		parts := []string{fmt.Sprintf("%v", queueURL)}
		if user := attrStr(a, "AWS_ACCESS_KEY_ID"); user != "" {
			parts = append(parts, "user="+user)
		}
		return strings.Join(parts, "  ")
	}

	// HTTP: make a clickable URL.
	if ep.Protocol == "http" {
		return "http://" + ep.HostPort
	}

	// Fallback: just the hostport.
	return ep.HostPort
}

// attrStr returns the string value of an attribute, or "" if missing.
func attrStr(attrs map[string]any, key string) string {
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
