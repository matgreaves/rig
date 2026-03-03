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
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/matgreaves/rig/internal/server/dockerutil"
	"github.com/matgreaves/run/onexit"
)

const sqsDefaultImage = "softwaremill/elasticmq-native:1.6.9"

// NewSQSPool creates a Pool backed by ElasticMQ containers. A single shared
// container per rigd process provides SQS-compatible messaging;
// individual test environments get isolated queues within it.
func NewSQSPool(pid int) *Pool {
	return NewPool(func(key string) Backend {
		return &sqsBackend{
			containerName: fmt.Sprintf("rig-sqs-%d", pid),
		}
	}, 10*time.Minute)
}

// sqsBackend implements Backend for ElasticMQ Docker containers.
type sqsBackend struct {
	containerName string
	containerID   string
	cancelOnexit  func() error

	host string
	port int

	queueN    atomic.Uint64
	sqsClient *sqs.Client
}

// Start creates and starts a shared ElasticMQ container.
func (b *sqsBackend) Start(ctx context.Context) (string, int, error) {
	cli, err := dockerutil.Client()
	if err != nil {
		return "", 0, fmt.Errorf("docker client: %w", err)
	}

	// If a same-name container exists (from a previous crash), remove it.
	cli.ContainerRemove(ctx, b.containerName, container.RemoveOptions{Force: true})

	containerPort := nat.Port("9324/tcp")

	config := &container.Config{
		Image:        sqsDefaultImage,
		ExposedPorts: nat.PortSet{containerPort: {}},
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
		return "", 0, fmt.Errorf("no port binding for 9324")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return "", 0, fmt.Errorf("parse host port: %w", err)
	}

	b.host = "127.0.0.1"
	b.port = port

	// Wait for ElasticMQ to be ready.
	if err := b.waitReady(ctx); err != nil {
		return "", 0, fmt.Errorf("wait for ready: %w", err)
	}

	// Create a reusable SQS client for queue management.
	b.sqsClient = sqs.New(sqs.Options{
		BaseEndpoint: aws.String(fmt.Sprintf("http://127.0.0.1:%d", port)),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("rig", "rig", ""),
	})

	return "127.0.0.1", port, nil
}

// Stop stops and removes the Docker container.
func (b *sqsBackend) Stop() {
	if b.containerID == "" {
		return
	}

	cli, err := dockerutil.Client()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	timeout := 10
	cli.ContainerStop(ctx, b.containerID, container.StopOptions{Timeout: &timeout})
	cli.ContainerRemove(ctx, b.containerID, container.RemoveOptions{Force: true})

	if b.cancelOnexit != nil {
		b.cancelOnexit()
	}
}

// NewLease creates a new SQS queue for per-test isolation.
// Returns the queue URL as ID and nil as Data.
func (b *sqsBackend) NewLease(ctx context.Context) (string, any, error) {
	n := b.queueN.Add(1)
	queueName := fmt.Sprintf("rig-%d", n)

	result, err := b.sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	if err != nil {
		return "", nil, fmt.Errorf("create queue %s: %w", queueName, err)
	}

	return *result.QueueUrl, nil, nil
}

// DropLease deletes the queue. Best-effort, errors ignored.
func (b *sqsBackend) DropLease(ctx context.Context, id string) {
	b.sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(id),
	})
}

// waitReady polls the ElasticMQ endpoint until it responds.
// ElasticMQ returns 400 on GET / (missing SQS Action parameter),
// which is sufficient to confirm the server is ready.
func (b *sqsBackend) waitReady(ctx context.Context) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", b.port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.After(60 * time.Second)

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				// ElasticMQ returns 400 (MissingAction) when healthy.
				if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("ElasticMQ not ready after 60s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
