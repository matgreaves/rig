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
	addrFile := filepath.Join(defaultRigDir(), "rigd.addr")
	data, err := os.ReadFile(addrFile)
	if err != nil {
		return "", fmt.Errorf("rigd is not running (no addr file at %s)", addrFile)
	}
	addr := strings.TrimSpace(string(data))
	if addr == "" {
		return "", fmt.Errorf("rigd addr file is empty")
	}
	return "http://" + addr, nil
}
