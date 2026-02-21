package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/matgreaves/rig/server/dockerutil"
)

// DockerPull resolves a Docker image by pulling it from a registry. The image
// reference (e.g. "postgres:16", "redis:7-alpine") is the version pin.
//
// The artifact output has no Path (the image lives in the Docker daemon, not
// on disk). Meta contains the image reference and resolved image ID.
type DockerPull struct {
	Image string // e.g. "postgres:16", "redis:7-alpine"
}

// CacheKey returns a stable hash of the image reference.
func (d DockerPull) CacheKey() (string, error) {
	raw := "docker:" + d.Image
	sum := sha256.Sum256([]byte(raw))
	return "docker/" + hex.EncodeToString(sum[:]), nil
}

// Cached checks for a breadcrumb file (.image-id) left by a previous Resolve.
func (d DockerPull) Cached(outputDir string) (Output, bool) {
	data, err := os.ReadFile(filepath.Join(outputDir, ".image-id"))
	if err != nil {
		return Output{}, false
	}
	imageID := strings.TrimSpace(string(data))
	if imageID == "" {
		return Output{}, false
	}
	return Output{
		Meta: map[string]string{
			"image":    d.Image,
			"image_id": imageID,
		},
	}, true
}

// Resolve pulls the image and writes a breadcrumb with the image ID.
func (d DockerPull) Resolve(ctx context.Context, outputDir string) (Output, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Output{}, fmt.Errorf("create output dir: %w", err)
	}

	cli, err := dockerutil.Client()
	if err != nil {
		return Output{}, fmt.Errorf("docker client: %w", err)
	}

	rc, err := cli.ImagePull(ctx, d.Image, image.PullOptions{})
	if err != nil {
		return Output{}, fmt.Errorf("docker pull %s: %w", d.Image, err)
	}
	// Drain the pull output to completion — the pull isn't done until
	// the response body is fully read.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		rc.Close()
		return Output{}, fmt.Errorf("docker pull %s: read response: %w", d.Image, err)
	}
	rc.Close()

	// Inspect to get the resolved image ID.
	inspect, _, err := cli.ImageInspectWithRaw(ctx, d.Image)
	if err != nil {
		return Output{}, fmt.Errorf("docker inspect %s: %w", d.Image, err)
	}

	imageID := inspect.ID

	// Write breadcrumb so Cached finds it next time.
	if err := os.WriteFile(filepath.Join(outputDir, ".image-id"), []byte(imageID), 0o644); err != nil {
		return Output{}, fmt.Errorf("write breadcrumb: %w", err)
	}

	return Output{
		Meta: map[string]string{
			"image":    d.Image,
			"image_id": imageID,
		},
	}, nil
}

// Retryable returns true — image pulls are network operations.
func (d DockerPull) Retryable() bool { return true }

// Valid checks whether the pulled image still exists in the local Docker
// daemon. Images can disappear via docker prune or manual removal.
// Implements artifact.Validator.
func (d DockerPull) Valid(output Output) bool {
	imageID := output.Meta["image_id"]
	if imageID == "" {
		return false
	}
	cli, err := dockerutil.Client()
	if err != nil {
		return false
	}

	_, _, err = cli.ImageInspectWithRaw(context.Background(), imageID)
	return err == nil
}
