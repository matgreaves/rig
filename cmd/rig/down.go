package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func runDown(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rig down <environment-name-or-id>")
	}

	target := args[0]

	addr, err := rigdata.ServerAddr(RigdVersion)
	if err != nil {
		return err
	}

	id, err := rigdata.ResolveEnvID(addr, target)
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
