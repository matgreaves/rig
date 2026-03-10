package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// serverAddr reads the rigd server address from the addr file.
// Returns an error if rigd is not running.
func serverAddr() (string, error) {
	rigDir := defaultRigDir()

	// Try versioned addr file first, fall back to unversioned (RIG_BINARY / legacy).
	candidates := []string{
		filepath.Join(rigDir, "rigd-v"+RigdVersion+".addr"),
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
