package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/run/onexit"
)

const (
	s3DefaultImage = "minio/minio:latest"
	s3AccessKey    = "rigadmin"
	s3SecretKey    = "rigadmin"
)

// NewS3Pool creates a Pool backed by MinIO containers. A single shared
// container per rigd process provides S3-compatible object storage;
// individual test environments get isolated buckets within it.
//
// The idle timeout is set longer than rigd's own 5-minute idle shutdown,
// so in practice the container lives for the lifetime of the rigd process.
// If rigd ever runs indefinitely, this timeout provides a safety net.
func NewS3Pool(pid int) *Pool {
	return NewPool(func(key string) Backend {
		return &s3Backend{
			containerName: fmt.Sprintf("rig-s3-%d", pid),
		}
	}, 10*time.Minute)
}

// s3Backend implements Backend for MinIO Docker containers.
type s3Backend struct {
	containerName string
	containerID   string
	cancelOnexit  func() error

	host string
	port int

	bucketN  atomic.Uint64
	s3Client *s3.Client
}

// Start creates and starts a shared MinIO container.
func (b *s3Backend) Start(ctx context.Context) (string, int, error) {
	cli, err := dockerutil.Client()
	if err != nil {
		return "", 0, fmt.Errorf("docker client: %w", err)
	}

	// If a same-name container exists (from a previous crash), remove it.
	cli.ContainerRemove(ctx, b.containerName, container.RemoveOptions{Force: true})

	containerPort := nat.Port("9000/tcp")

	config := &container.Config{
		Image:        s3DefaultImage,
		Cmd:          []string{"server", "/data"},
		ExposedPorts: nat.PortSet{containerPort: {}},
		Env: []string{
			"MINIO_ROOT_USER=" + s3AccessKey,
			"MINIO_ROOT_PASSWORD=" + s3SecretKey,
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			containerPort: []nat.PortBinding{{
				HostIP:   "127.0.0.1",
				HostPort: "", // Docker auto-assigns
			}},
		},
	}

	resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, b.containerName)
	if err != nil {
		return "", 0, fmt.Errorf("create container: %w", err)
	}
	b.containerID = resp.ID

	// Register onexit cleanup.
	cancelOnexit, _ := onexit.OnExitF("docker rm -f %s", b.containerID)
	b.cancelOnexit = cancelOnexit

	if err := cli.ContainerStart(ctx, b.containerID, container.StartOptions{}); err != nil {
		return "", 0, fmt.Errorf("start container: %w", err)
	}

	// Read back the mapped host port.
	inspect, err := cli.ContainerInspect(ctx, b.containerID)
	if err != nil {
		return "", 0, fmt.Errorf("inspect container: %w", err)
	}

	bindings, ok := inspect.NetworkSettings.Ports[containerPort]
	if !ok || len(bindings) == 0 {
		return "", 0, fmt.Errorf("no port binding for 9000")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return "", 0, fmt.Errorf("parse host port: %w", err)
	}

	b.host = "127.0.0.1"
	b.port = port

	// Wait for MinIO to be ready.
	if err := b.waitReady(ctx); err != nil {
		return "", 0, fmt.Errorf("wait for ready: %w", err)
	}

	// Create a reusable S3 client for bucket management.
	// BaseEndpoint overrides the default S3 endpoint; UsePathStyle
	// prevents bucket-subdomain rewriting.
	b.s3Client = s3.New(s3.Options{
		BaseEndpoint: aws.String(fmt.Sprintf("http://127.0.0.1:%d", port)),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(s3AccessKey, s3SecretKey, ""),
		UsePathStyle: true,
	})

	return "127.0.0.1", port, nil
}

// Stop stops and removes the Docker container.
func (b *s3Backend) Stop() {
	if b.containerID == "" {
		return
	}

	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	timeout := 5
	cli.ContainerStop(ctx, b.containerID, container.StopOptions{Timeout: &timeout})
	cli.ContainerRemove(ctx, b.containerID, container.RemoveOptions{Force: true})

	if b.cancelOnexit != nil {
		b.cancelOnexit()
	}
}

// NewLease creates a new S3 bucket for per-test isolation.
// Returns the bucket name as ID and nil as Data.
func (b *s3Backend) NewLease(ctx context.Context) (string, any, error) {
	n := b.bucketN.Add(1)
	bucket := fmt.Sprintf("rig-%d", n)

	_, err := b.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return "", nil, fmt.Errorf("create bucket %s: %w", bucket, err)
	}

	return bucket, nil, nil
}

// DropLease empties and deletes the bucket. Best-effort, errors ignored.
func (b *s3Backend) DropLease(ctx context.Context, id string) {
	// List and delete all objects in the bucket.
	paginator := s3.NewListObjectsV2Paginator(b.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(id),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			break
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]s3types.ObjectIdentifier, len(page.Contents))
		for i, obj := range page.Contents {
			objects[i] = s3types.ObjectIdentifier{Key: obj.Key}
		}
		b.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(id),
			Delete: &s3types.Delete{Objects: objects},
		})
	}

	// Delete the bucket.
	b.s3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(id),
	})
}

// waitReady polls the MinIO health endpoint until it responds.
func (b *s3Backend) waitReady(ctx context.Context) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/minio/health/live", b.port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.After(60 * time.Second)

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("MinIO not ready after 60s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
