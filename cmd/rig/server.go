package main

import "github.com/matgreaves/rig/cmd/rig/rigdata"

// serverAddr returns the rigd server address, using the version from this CLI.
// Kept as a convenience wrapper used by commands that haven't been migrated
// to call rigdata.ServerAddr directly.
func serverAddr() (string, error) {
	return rigdata.ServerAddr(RigdVersion)
}
