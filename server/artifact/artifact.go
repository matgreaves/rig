// Package artifact provides the artifact resolution system for rig.
// Before any services start, all artifacts (compiled binaries, pulled Docker
// images, downloaded files) are resolved in parallel. Results are cached
// content-addressably so subsequent runs are instant when inputs haven't changed.
package artifact

import "context"

// Artifact describes a single artifact that must be resolved before services start.
// Key is a globally unique identifier used for deduplication across a single
// Orchestrate call — if two services reference the same artifact key, it is
// resolved only once.
type Artifact struct {
	Key      string   // dedup key (e.g. "gobuild:/abs/path/to/module")
	Resolver Resolver // knows how to build/pull/download
}

// Output is the result of a successful artifact resolution.
type Output struct {
	Path string            // local path to the resolved artifact (binary, download); empty for non-file artifacts (docker images)
	Meta map[string]string // type-specific metadata (e.g. module name, image digest)
}

// Resolver knows how to produce an Artifact output.
type Resolver interface {
	// CacheKey returns a stable content-based key used to locate the on-disk
	// cache directory for this artifact. For local builds this is a hash of
	// the source tree; for network artifacts it is derived from the version pin.
	CacheKey() (string, error)

	// Cached checks whether a previously resolved artifact exists in outputDir.
	// Each resolver knows what evidence to look for — a compiled binary, a
	// breadcrumb file, etc. Returns the reconstructed Output and true on a hit,
	// or zero value and false on a miss.
	//
	// This replaces a generic meta.json cache: the resolver owns both producing
	// and checking its own artifacts.
	Cached(outputDir string) (Output, bool)

	// Resolve produces the artifact in outputDir. The caller guarantees that
	// Resolve is only called after Cached returned false. The resolver writes
	// whatever it needs into outputDir so that a future Cached call will
	// find the result.
	Resolve(ctx context.Context, outputDir string) (Output, error)

	// Retryable reports whether transient errors during Resolve should be
	// retried. Returns true for network-dependent operations (docker pull,
	// download), false for local operations (go build, command).
	Retryable() bool
}

// Validator is an optional interface for resolvers whose artifacts can
// disappear externally — Docker images can be pruned, downloaded binaries
// can be deleted by other tools. When a Resolver also implements Validator,
// the resolution framework calls Valid after a successful Cached check.
// If Valid returns false, the cached result is discarded and the artifact
// is re-resolved.
//
// Resolvers whose artifacts live entirely within rig's cache directory
// (GoBuild, Download) do not need this — those files only disappear if
// rig itself cleans them up.
type Validator interface {
	Valid(output Output) bool
}
