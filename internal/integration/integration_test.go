package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	rig "github.com/matgreaves/rig/client"
	"github.com/matgreaves/rig/connect"
	"github.com/matgreaves/rig/connect/httpx"
	"github.com/matgreaves/rig/internal/server"
	"github.com/matgreaves/rig/internal/server/service"
	"github.com/matgreaves/rig/internal/testdata/services/echo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

var sharedServerURL string

// moduleRoot returns the internal module root by finding go.mod relative to
// the test working directory.
func moduleRoot(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

func TestMain(m *testing.M) {
	// Find module root without testing.TB.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fmt.Fprintf(os.Stderr, "could not find go.mod\n")
			os.Exit(1)
		}
		dir = parent
	}

	pgPool := service.NewPostgresPool(os.Getpid())
	redisPool := service.NewRedisPool(os.Getpid())
	s3Pool := service.NewS3Pool(os.Getpid())
	sqsPool := service.NewSQSPool(os.Getpid())

	cacheDir := filepath.Join(dir, "..", ".rig", "cache")
	temporalPool := service.NewTemporalPool(cacheDir)

	reg := service.NewRegistry()
	reg.Register("process", service.Process{})
	reg.Register("go", service.Go{})
	reg.Register("client", service.Client{})
	reg.Register("container", service.Container{})
	reg.Register("postgres", service.NewPostgres(pgPool))
	reg.Register("redis", service.NewRedis(redisPool))
	reg.Register("temporal", service.NewTemporal(temporalPool))
	reg.Register("s3", service.NewS3(s3Pool))
	reg.Register("sqs", service.NewSQS(sqsPool))
	reg.Register("proxy", service.NewProxy())
	reg.Register("test", service.Test{})

	rigDir := filepath.Join(dir, "..", ".rig")
	tmpDir, err := os.MkdirTemp("", "rig-integration-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %v\n", err)
		os.Exit(1)
	}

	s := server.NewServer(
		server.NewPortAllocator(),
		reg,
		tmpDir,
		0, // idle timeout disabled
		rigDir,
	)
	ts := httptest.NewServer(s)
	sharedServerURL = ts.URL

	code := m.Run()

	ts.Close()
	temporalPool.Close()
	sqsPool.Close()
	s3Pool.Close()
	redisPool.Close()
	pgPool.Close()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

// repoRoot returns the top-level repo directory (parent of internal/).
func repoRoot(t testing.TB) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "..")
}

// TestUp runs all integration tests against a shared rig server. Each subtest
// creates its own environment in parallel — exactly how real users would use rig.
func TestUp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	serverURL := sharedServerURL

	t.Run("GoService", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"echo": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("ProcessService", func(t *testing.T) {
		t.Parallel()

		// Build the echo binary first since "process" type needs a pre-built binary.
		echoBin := buildBinary(t, filepath.Join(root, "internal", "testdata", "services", "echo", "cmd"))

		env := rig.Up(t, rig.Services{
			"echo": rig.Process(echoBin),
		}, rig.WithServer(serverURL), rig.WithTimeout(30*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("WithDependency", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
				EgressAs("database", "db"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// API should be reachable.
		client := httpx.New(env.Endpoint("api"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("api health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("api health: %d, want 200", resp.StatusCode)
		}

		// DB should be reachable via TCP.
		conn, err := net.DialTimeout("tcp", env.Endpoint("db").HostPort, 2*time.Second)
		if err != nil {
			t.Fatalf("db dial: %v", err)
		}
		conn.Close()
	})

	t.Run("InitHook", func(t *testing.T) {
		t.Parallel()

		var hookCalled bool
		var wiringSnapshot rig.Wiring

		env := rig.Up(t, rig.Services{
			"echo": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					hookCalled = true
					wiringSnapshot = w
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !hookCalled {
			t.Fatal("init hook was not called")
		}

		// Init hooks receive ingresses.
		if len(wiringSnapshot.Ingresses) == 0 {
			t.Error("init hook received no ingresses")
		}

		// Init hooks do NOT receive egresses.
		if len(wiringSnapshot.Egresses) != 0 {
			t.Errorf("init hook received egresses (should be empty): %v", wiringSnapshot.Egresses)
		}

		// Service should be reachable.
		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("PrestartHook", func(t *testing.T) {
		t.Parallel()

		var wiringSnapshot rig.Wiring

		rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
				EgressAs("database", "db").
				PrestartHook(func(ctx context.Context, w rig.Wiring) error {
					wiringSnapshot = w
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// Prestart hooks receive egresses.
		if _, ok := wiringSnapshot.Egresses["database"]; !ok {
			t.Error("prestart hook did not receive 'database' egress")
		}

		// Prestart hooks also receive ingresses.
		if len(wiringSnapshot.Ingresses) == 0 {
			t.Error("prestart hook received no ingresses")
		}
	})

	t.Run("NoIngressWorker", func(t *testing.T) {
		t.Parallel()

		// A service with no ingresses (worker pattern) should still become
		// ready. Verifies the lifecycle handles the no-health-check path.
		env := rig.Up(t, rig.Services{
			"worker": rig.Go(filepath.Join(root, "internal", "testdata", "services", "echo", "cmd")).
				NoIngress(),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// No endpoints to hit — just verify the environment came up with
		// the worker listed.
		if _, ok := env.Services["worker"]; !ok {
			t.Error("worker service not in resolved environment")
		}
	})

	t.Run("FuncService", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"echo": rig.Func(echo.Run),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("FuncServiceWithEgress", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
				Ingress("default", rig.IngressTCP()),
			"api": rig.Func(echo.Run).
				EgressAs("database", "db"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		// API should be reachable.
		client := httpx.New(env.Endpoint("api"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("api health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("api health: %d, want 200", resp.StatusCode)
		}

		// DB should be reachable via TCP.
		conn, err := net.DialTimeout("tcp", env.Endpoint("db").HostPort, 2*time.Second)
		if err != nil {
			t.Fatalf("db dial: %v", err)
		}
		conn.Close()
	})

	t.Run("FuncServiceWithInitHook", func(t *testing.T) {
		t.Parallel()

		var hookCalled bool

		env := rig.Up(t, rig.Services{
			"echo": rig.Func(echo.Run).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					hookCalled = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !hookCalled {
			t.Fatal("init hook was not called")
		}

		client := httpx.New(env.Endpoint("echo"))
		resp, err := client.Get("/health")
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("Container", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"nginx": rig.Container("nginx:alpine").Port(80),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		ep := env.Endpoint("nginx")
		resp, err := http.Get("http://" + ep.HostPort + "/")
		if err != nil {
			t.Fatalf("nginx request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("nginx status: %d, want 200", resp.StatusCode)
		}
	})

	t.Run("Postgres", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep := env.Endpoint("db")

		// Verify TCP connectivity.
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("postgres dial: %v", err)
		}
		conn.Close()

		// Verify endpoint attributes.
		if got := ep.Attr("PGDATABASE"); got == "" {
			t.Error("PGDATABASE is empty")
		}
		if got := ep.Attr("PGUSER"); got != "postgres" {
			t.Errorf("PGUSER = %q, want postgres", got)
		}
		if got := ep.Attr("PGPASSWORD"); got != "postgres" {
			t.Errorf("PGPASSWORD = %q, want postgres", got)
		}
		if got := ep.Attr("PGHOST"); got != "127.0.0.1" {
			t.Errorf("PGHOST = %q, want 127.0.0.1", got)
		}
		if got := ep.Attr("PGPORT"); got == "" {
			t.Error("PGPORT is empty")
		}
	})

	t.Run("PostgresSharedContainer", func(t *testing.T) {
		t.Parallel()

		// Two concurrent Postgres envs should share a single container
		// but get isolated databases with different data.
		env1 := rig.Up(t, rig.Services{
			"db": rig.Postgres().InitSQL(
				"CREATE TABLE items (id INT PRIMARY KEY, name TEXT)",
				"INSERT INTO items VALUES (1, 'from-env1')",
			),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		env2 := rig.Up(t, rig.Services{
			"db": rig.Postgres().InitSQL(
				"CREATE TABLE items (id INT PRIMARY KEY, name TEXT)",
				"INSERT INTO items VALUES (1, 'from-env2')",
			),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep1 := env1.Endpoint("db")
		ep2 := env2.Endpoint("db")

		// Isolated databases: different PGDATABASE values.
		db1 := ep1.Attr("PGDATABASE")
		db2 := ep2.Attr("PGDATABASE")
		if db1 == db2 {
			t.Fatalf("expected different databases, both got %s", db1)
		}
		if !strings.HasPrefix(db1, "rig_") {
			t.Errorf("db1 = %q, want rig_* prefix", db1)
		}
		if !strings.HasPrefix(db2, "rig_") {
			t.Errorf("db2 = %q, want rig_* prefix", db2)
		}

		// Both environments should be reachable.
		conn1, err := net.DialTimeout("tcp", ep1.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env1 dial: %v", err)
		}
		conn1.Close()

		conn2, err := net.DialTimeout("tcp", ep2.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env2 dial: %v", err)
		}
		conn2.Close()

		t.Logf("shared container: db1=%s, db2=%s", db1, db2)
	})

	t.Run("Temporal", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"temporal": rig.Temporal(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		// gRPC port reachable.
		ep := env.Endpoint("temporal")
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("temporal dial: %v", err)
		}
		conn.Close()

		// Attributes.
		if got := ep.Attr("TEMPORAL_ADDRESS"); got == "" {
			t.Error("TEMPORAL_ADDRESS is empty")
		}
		ns := ep.Attr("TEMPORAL_NAMESPACE")
		if !strings.HasPrefix(ns, "rig_ns_") {
			t.Errorf("TEMPORAL_NAMESPACE = %q, want rig_ns_* prefix", ns)
		}

		// UI reachable.
		uiEP := env.Endpoint("temporal", "ui")
		resp, err := http.Get("http://" + uiEP.HostPort)
		if err != nil {
			t.Fatalf("temporal UI request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("temporal UI status: %d, want < 500", resp.StatusCode)
		}
	})

	t.Run("TemporalSharedServer", func(t *testing.T) {
		t.Parallel()

		// Two concurrent Temporal envs should share a single process
		// but get isolated namespaces.
		env1 := rig.Up(t, rig.Services{
			"temporal": rig.Temporal(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		env2 := rig.Up(t, rig.Services{
			"temporal": rig.Temporal(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep1 := env1.Endpoint("temporal")
		ep2 := env2.Endpoint("temporal")

		// Isolated namespaces: different TEMPORAL_NAMESPACE values.
		ns1 := ep1.Attr("TEMPORAL_NAMESPACE")
		ns2 := ep2.Attr("TEMPORAL_NAMESPACE")
		if ns1 == ns2 {
			t.Fatalf("expected different namespaces, both got %s", ns1)
		}
		if !strings.HasPrefix(ns1, "rig_ns_") {
			t.Errorf("ns1 = %q, want rig_ns_* prefix", ns1)
		}
		if !strings.HasPrefix(ns2, "rig_ns_") {
			t.Errorf("ns2 = %q, want rig_ns_* prefix", ns2)
		}

		// Both environments should be reachable on the same gRPC port.
		conn1, err := net.DialTimeout("tcp", ep1.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env1 dial: %v", err)
		}
		conn1.Close()

		conn2, err := net.DialTimeout("tcp", ep2.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env2 dial: %v", err)
		}
		conn2.Close()

		t.Logf("shared server: ns1=%s, ns2=%s", ns1, ns2)
	})

	t.Run("Redis", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"cache": rig.Redis(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep := env.Endpoint("cache")

		// Verify TCP connectivity.
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("redis dial: %v", err)
		}
		conn.Close()

		// Verify endpoint attributes.
		redisURL := ep.Attr("REDIS_URL")
		if redisURL == "" {
			t.Error("REDIS_URL is empty")
		}
		if !strings.HasPrefix(redisURL, "redis://") {
			t.Errorf("REDIS_URL = %q, want redis:// prefix", redisURL)
		}
	})

	t.Run("RedisSharedContainer", func(t *testing.T) {
		t.Parallel()

		// Two concurrent Redis envs should share a single container
		// but get isolated databases with different data.
		env1 := rig.Up(t, rig.Services{
			"cache": rig.Redis(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		env2 := rig.Up(t, rig.Services{
			"cache": rig.Redis(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep1 := env1.Endpoint("cache")
		ep2 := env2.Endpoint("cache")

		// Isolated databases: different REDIS_URL values.
		url1 := ep1.Attr("REDIS_URL")
		url2 := ep2.Attr("REDIS_URL")
		if url1 == url2 {
			t.Fatalf("expected different REDIS_URLs, both got %s", url1)
		}

		// Both environments should be reachable.
		conn1, err := net.DialTimeout("tcp", ep1.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env1 dial: %v", err)
		}
		conn1.Close()

		conn2, err := net.DialTimeout("tcp", ep2.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env2 dial: %v", err)
		}
		conn2.Close()

		t.Logf("shared container: url1=%s, url2=%s", url1, url2)
	})

	t.Run("S3", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"storage": rig.S3(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep := env.Endpoint("storage")

		// Verify TCP connectivity.
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("s3 dial: %v", err)
		}
		conn.Close()

		// Verify endpoint attributes.
		s3Endpoint := ep.Attr("S3_ENDPOINT")
		if s3Endpoint == "" {
			t.Error("S3_ENDPOINT is empty")
		}
		if !strings.HasPrefix(s3Endpoint, "http://") {
			t.Errorf("S3_ENDPOINT = %q, want http:// prefix", s3Endpoint)
		}
		s3Bucket := ep.Attr("S3_BUCKET")
		if s3Bucket == "" {
			t.Error("S3_BUCKET is empty")
		}
		if !strings.HasPrefix(s3Bucket, "rig-") {
			t.Errorf("S3_BUCKET = %q, want rig-* prefix", s3Bucket)
		}

		// Build S3 client from endpoint attributes (same pattern as s3x.Connect).
		s3Client := s3.New(s3.Options{
			BaseEndpoint: aws.String(s3Endpoint),
			Region:       "us-east-1",
			Credentials:  credentials.NewStaticCredentialsProvider(ep.Attr("AWS_ACCESS_KEY_ID"), ep.Attr("AWS_SECRET_ACCESS_KEY"), ""),
			UsePathStyle: true,
		})

		// Verify we can PutObject and GetObject.
		_, err = s3Client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: aws.String(s3Bucket),
			Key:    aws.String("test-object.txt"),
			Body:   strings.NewReader("hello s3"),
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}

		getResult, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: aws.String(s3Bucket),
			Key:    aws.String("test-object.txt"),
		})
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		defer getResult.Body.Close()
		body, _ := io.ReadAll(getResult.Body)
		if string(body) != "hello s3" {
			t.Errorf("GetObject body = %q, want %q", body, "hello s3")
		}
	})

	t.Run("S3SharedContainer", func(t *testing.T) {
		t.Parallel()

		env1 := rig.Up(t, rig.Services{
			"storage": rig.S3(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		env2 := rig.Up(t, rig.Services{
			"storage": rig.S3(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep1 := env1.Endpoint("storage")
		ep2 := env2.Endpoint("storage")

		// Isolated buckets: different S3_BUCKET values.
		bucket1 := ep1.Attr("S3_BUCKET")
		bucket2 := ep2.Attr("S3_BUCKET")
		if bucket1 == bucket2 {
			t.Fatalf("expected different buckets, both got %s", bucket1)
		}
		if !strings.HasPrefix(bucket1, "rig-") {
			t.Errorf("bucket1 = %q, want rig-* prefix", bucket1)
		}
		if !strings.HasPrefix(bucket2, "rig-") {
			t.Errorf("bucket2 = %q, want rig-* prefix", bucket2)
		}

		// Both environments should be reachable.
		conn1, err := net.DialTimeout("tcp", ep1.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env1 dial: %v", err)
		}
		conn1.Close()

		conn2, err := net.DialTimeout("tcp", ep2.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env2 dial: %v", err)
		}
		conn2.Close()

		t.Logf("shared container: bucket1=%s, bucket2=%s", bucket1, bucket2)
	})

	t.Run("SQS", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"queue": rig.SQS(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep := env.Endpoint("queue")

		// Verify TCP connectivity.
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("sqs dial: %v", err)
		}
		conn.Close()

		// Verify endpoint attributes.
		sqsEndpoint := ep.Attr("SQS_ENDPOINT")
		if sqsEndpoint == "" {
			t.Error("SQS_ENDPOINT is empty")
		}
		if !strings.HasPrefix(sqsEndpoint, "http://") {
			t.Errorf("SQS_ENDPOINT = %q, want http:// prefix", sqsEndpoint)
		}
		sqsQueueURL := ep.Attr("SQS_QUEUE_URL")
		if sqsQueueURL == "" {
			t.Error("SQS_QUEUE_URL is empty")
		}

		// Build SQS client from endpoint attributes.
		sqsClient := sqs.New(sqs.Options{
			BaseEndpoint: aws.String(sqsEndpoint),
			Region:       "us-east-1",
			Credentials:  credentials.NewStaticCredentialsProvider(ep.Attr("AWS_ACCESS_KEY_ID"), ep.Attr("AWS_SECRET_ACCESS_KEY"), ""),
		})

		// Verify we can send and receive a message.
		_, err = sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
			QueueUrl:    aws.String(sqsQueueURL),
			MessageBody: aws.String("hello sqs"),
		})
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}

		recvResult, err := sqsClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(sqsQueueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     5,
		})
		if err != nil {
			t.Fatalf("ReceiveMessage: %v", err)
		}
		if len(recvResult.Messages) == 0 {
			t.Fatal("ReceiveMessage returned no messages")
		}
		if got := *recvResult.Messages[0].Body; got != "hello sqs" {
			t.Errorf("message body = %q, want %q", got, "hello sqs")
		}
	})

	t.Run("SQSSharedContainer", func(t *testing.T) {
		t.Parallel()

		env1 := rig.Up(t, rig.Services{
			"queue": rig.SQS(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		env2 := rig.Up(t, rig.Services{
			"queue": rig.SQS(),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		ep1 := env1.Endpoint("queue")
		ep2 := env2.Endpoint("queue")

		// Isolated queues: different SQS_QUEUE_URL values.
		url1 := ep1.Attr("SQS_QUEUE_URL")
		url2 := ep2.Attr("SQS_QUEUE_URL")
		if url1 == url2 {
			t.Fatalf("expected different queue URLs, both got %s", url1)
		}

		// Both environments should be reachable.
		conn1, err := net.DialTimeout("tcp", ep1.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env1 dial: %v", err)
		}
		conn1.Close()

		conn2, err := net.DialTimeout("tcp", ep2.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("env2 dial: %v", err)
		}
		conn2.Close()

		t.Logf("shared container: url1=%s, url2=%s", url1, url2)
	})

	t.Run("PostgresInitSQL_BadSQL", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"db": rig.Postgres().InitSQL("INSERT INTO nonexistent_table VALUES (1)"),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))
		if err == nil {
			t.Fatal("expected Up to fail due to bad SQL")
		}

		t.Logf("captured failure: %s", err)
	})

	t.Run("PostgresInitSQL", func(t *testing.T) {
		t.Parallel()

		// Use a second InitSQL to verify the first one ran — inserting into
		// the table created by the first statement proves both executed
		// in order. A follow-up InitHook verifies from the client side.
		var initHookRan bool

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres().
				InitSQL(
					"CREATE TABLE test_init (id INT PRIMARY KEY, name TEXT NOT NULL)",
					"INSERT INTO test_init VALUES (1, 'hello')",
				).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					initHookRan = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

		if !initHookRan {
			t.Fatal("init hook was not called after InitSQL")
		}

		// Verify service is reachable.
		ep := env.Endpoint("db")
		conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
		if err != nil {
			t.Fatalf("postgres dial: %v", err)
		}
		conn.Close()
	})

	t.Run("UserAPI", func(t *testing.T) {
		t.Parallel()

		env := rig.Up(t, rig.Services{
			"db": rig.Postgres().InitSQL(
				"CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)",
			),
			"api": rig.Go(filepath.Join(root, "internal", "testdata", "services", "userapi")).
				Egress("db"),
		}, rig.WithServer(serverURL))

		api := httpx.New(env.Endpoint("api"))

		// Create a user.
		resp, err := api.Post("/users", "application/json",
			strings.NewReader(`{"name":"Alice"}`))
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create user: status %d, want 201", resp.StatusCode)
		}
		var created user
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("decode created user: %v", err)
		}
		if created.Name != "Alice" || created.ID == 0 {
			t.Fatalf("created user = %+v, want name=Alice id>0", created)
		}

		// Read the user back.
		resp2, err := api.Get(fmt.Sprintf("/users/%d", created.ID))
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("get user: status %d, want 200", resp2.StatusCode)
		}
		var fetched user
		if err := json.NewDecoder(resp2.Body).Decode(&fetched); err != nil {
			t.Fatalf("decode fetched user: %v", err)
		}
		if fetched != created {
			t.Fatalf("fetched user = %+v, want %+v", fetched, created)
		}

		// Delete the user.
		req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("/users/%d", created.ID), nil)
		resp3, err := api.Do(req)
		if err != nil {
			t.Fatalf("delete user: %v", err)
		}
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusNoContent {
			t.Fatalf("delete user: status %d, want 204", resp3.StatusCode)
		}

		// Confirm it's gone.
		resp4, err := api.Get(fmt.Sprintf("/users/%d", created.ID))
		if err != nil {
			t.Fatalf("get deleted user: %v", err)
		}
		resp4.Body.Close()
		if resp4.StatusCode != http.StatusNotFound {
			t.Fatalf("get deleted user: status %d, want 404", resp4.StatusCode)
		}
	})

	t.Run("ContainerExecHook", func(t *testing.T) {
		t.Parallel()

		// Use nginx:alpine which has a working HTTP server — this ensures
		// the container is healthy (HTTP check passes) before exec hooks run.
		// The exec hook writes a file; a client-side init hook verifies it ran.
		var verified bool
		env := rig.Up(t, rig.Services{
			"box": rig.Container("nginx:alpine").Port(80).
				Exec("sh", "-c", "echo hello > /tmp/exec-test").
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					verified = true
					return nil
				}),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if !verified {
			t.Fatal("client-side init hook was not called (exec hook may have failed)")
		}

		if _, ok := env.Services["box"]; !ok {
			t.Error("box service not in resolved environment")
		}
	})

	t.Run("ContainerExecHookFailure", func(t *testing.T) {
		t.Parallel()

		// Exec a command that will fail (nonexistent binary).
		_, err := rig.TryUp(t, rig.Services{
			"box": rig.Container("nginx:alpine").Port(80).
				Exec("nonexistent-binary"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))
		if err == nil {
			t.Fatal("expected Up to fail due to exec hook error")
		}
		t.Logf("captured failure: %s", err)
	})

	t.Run("ContainerExecHookNoIngress", func(t *testing.T) {
		t.Parallel()

		// A no-ingress container with an exec hook. Without waitForContainer,
		// the exec hook races with container creation and fails because the
		// container doesn't exist yet when docker exec is called.
		env := rig.Up(t, rig.Services{
			"box": rig.Container("nginx:alpine").
				NoIngress().
				Exec("sh", "-c", "echo hello > /tmp/exec-test"),
		}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

		if _, ok := env.Services["box"]; !ok {
			t.Error("box service not in resolved environment")
		}
	})

	t.Run("InitHookFailure", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"echo": rig.Func(echo.Run).
				InitHook(func(ctx context.Context, w rig.Wiring) error {
					return fmt.Errorf("deliberate init failure")
				}),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to init hook error")
		}
		if !strings.Contains(err.Error(), "deliberate init failure") {
			t.Errorf("error does not mention hook failure: %v", err)
		}
	})

	t.Run("PrestartHookFailure", func(t *testing.T) {
		t.Parallel()

		_, err := rig.TryUp(t, rig.Services{
			"db": rig.Func(echo.Run),
			"api": rig.Func(echo.Run).
				Egress("db").
				PrestartHook(func(ctx context.Context, w rig.Wiring) error {
					return fmt.Errorf("deliberate prestart failure")
				}),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to prestart hook error")
		}
		if !strings.Contains(err.Error(), "deliberate prestart failure") {
			t.Errorf("error does not mention hook failure: %v", err)
		}
	})

	t.Run("StartupTimeout", func(t *testing.T) {
		t.Parallel()

		// A Func that blocks without listening — the health check will
		// never pass, so Up should fail when the timeout expires.
		start := time.Now()
		_, err := rig.TryUp(t, rig.Services{
			"stuck": rig.Func(func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}),
		}, rig.WithServer(serverURL), rig.WithTimeout(3*time.Second))
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected Up to fail due to timeout")
		}
		// Should fail around 3s, not hang.
		if elapsed > 10*time.Second {
			t.Errorf("timeout took too long: %v (want ~3s)", elapsed)
		}
	})

	t.Run("ServiceCrash", func(t *testing.T) {
		t.Parallel()

		// The fail service exits immediately with an error. The
		// environment should fail with a clear message.
		_, err := rig.TryUp(t, rig.Services{
			"crasher": rig.Go(filepath.Join(root, "internal", "testdata", "services", "fail")),
		}, rig.WithServer(serverURL))
		if err == nil {
			t.Fatal("expected Up to fail due to service crash")
		}
		if !strings.Contains(err.Error(), "crasher") {
			t.Errorf("error does not mention service name: %v", err)
		}
	})
}

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TestWrappedTB verifies that env.T captures assertion failures as test.note
// events in the server's event log.
func TestWrappedTB(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	// Post a test.note event directly and verify it appears in the log.
	// We avoid using env.T.Errorf in the main test because it would mark
	// the test as failed. Instead we POST the event ourselves — the same
	// code path env.T uses internally.
	env := rig.Up(t, rig.Services{
		"echo": rig.Func(echo.Run),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

	// Post a test.note event (same as env.T.Errorf would).
	payload, _ := json.Marshal(map[string]string{
		"type":  "test.note",
		"error": "simulated assertion: got 500, want 200",
	})
	noteResp, err := http.Post(
		fmt.Sprintf("%s/environments/%s/events", serverURL, env.ID),
		"application/json",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		t.Fatalf("post test.note: %v", err)
	}
	noteResp.Body.Close()

	// Fetch the event log and verify the test.note event appears.
	resp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer resp.Body.Close()

	var events []struct {
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Type == "test.note" && strings.Contains(e.Error, "simulated assertion") {
			found = true
			break
		}
	}
	if !found {
		t.Error("test.note event not found in event log")
	}
}

// TestObserve verifies that observe mode (on by default) inserts transparent
// traffic proxies and captures request events in the event log.
func TestObserve(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	// Two services: api (Func) depends on backend (Func).
	// Both are HTTP so we get request.completed events.
	env := rig.Up(t, rig.Services{
		"backend": rig.Func(echo.Run),
		"api":     rig.Func(echo.Run).EgressAs("backend", "backend"),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

	// Make requests through the external proxy (env.Endpoint returns proxy).
	client := httpx.New(env.Endpoint("api"))
	for range 3 {
		resp, err := client.Get("/hello")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d, want 200", resp.StatusCode)
		}
	}

	// Also hit the backend directly through its external proxy.
	backendClient := httpx.New(env.Endpoint("backend"))
	resp, err := backendClient.Get("/direct")
	if err != nil {
		t.Fatalf("backend request: %v", err)
	}
	resp.Body.Close()

	// Fetch the event log and verify request.completed events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type    string `json:"type"`
		Request *struct {
			Source     string `json:"source"`
			Target     string `json:"target"`
			Method     string `json:"method"`
			Path       string `json:"path"`
			StatusCode int    `json:"status_code"`
		} `json:"request,omitempty"`
		Endpoint *struct {
			Port int `json:"port"`
		} `json:"endpoint,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	// Count request.completed events by source.
	// With proxy-as-service, the external proxy source is "~test" and
	// the egress proxy source is the consuming service name.
	testToAPI := 0
	testToBackend := 0
	for _, e := range events {
		if e.Type != "request.completed" || e.Request == nil {
			continue
		}
		if e.Request.Source == "~test" && e.Request.Target == "api" {
			testToAPI++
		}
		if e.Request.Source == "~test" && e.Request.Target == "backend" {
			testToBackend++
		}
	}

	// We made 3 requests to api + health check requests from ready polling.
	// At minimum we should see our 3 explicit requests.
	if testToAPI < 3 {
		t.Errorf("~test→api requests: got %d, want >= 3", testToAPI)
	}
	if testToBackend < 1 {
		t.Errorf("~test→backend requests: got %d, want >= 1", testToBackend)
	}

}

// TestObserveAttributes verifies that the observe proxy rewrites
// address-derived endpoint attributes (TEMPORAL_ADDRESS) so that tools
// reading env vars go through the proxy, not the real service.
func TestObserveAttributes(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"temporal": rig.Temporal(),
	}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

	ep := env.Endpoint("temporal")

	// TEMPORAL_ADDRESS must match the proxy endpoint, not the real service.
	wantAddr := fmt.Sprintf("127.0.0.1:%d", ep.Port())
	if got := ep.Attr("TEMPORAL_ADDRESS"); got != wantAddr {
		t.Errorf("TEMPORAL_ADDRESS = %q, want %q (proxy address)", got, wantAddr)
	}

	// Non-address attrs should be unchanged (pool-assigned namespace).
	if got := ep.Attr("TEMPORAL_NAMESPACE"); !strings.HasPrefix(got, "rig_ns_") {
		t.Errorf("TEMPORAL_NAMESPACE = %q, want rig_ns_* prefix", got)
	}

	// Verify TCP connectivity through the proxy.
	conn, err := net.DialTimeout("tcp", ep.HostPort, 5*time.Second)
	if err != nil {
		t.Fatalf("temporal dial through proxy: %v", err)
	}
	conn.Close()
}

// TestObserveTCP verifies TCP proxy captures connection events.
func TestObserveTCP(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"tcpecho": rig.Go(filepath.Join(root, "internal", "testdata", "services", "tcpecho")).
			Ingress("default", rig.IngressTCP()),
	}, rig.WithServer(serverURL), rig.WithTimeout(60*time.Second))

	// Connect through the proxy.
	ep := env.Endpoint("tcpecho")
	conn, err := net.DialTimeout("tcp", ep.HostPort, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, err = conn.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	t.Logf("tcpecho response: %s", buf[:n])
	conn.Close()

	// Give the proxy a moment to emit the connection.closed event.
	time.Sleep(100 * time.Millisecond)

	// Fetch event log and verify connection events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type       string `json:"type"`
		Connection *struct {
			Source  string `json:"source"`
			Target  string `json:"target"`
			BytesIn int64  `json:"bytes_in"`
		} `json:"connection,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var opened, closed int
	for _, e := range events {
		switch e.Type {
		case "connection.opened":
			opened++
		case "connection.closed":
			if e.Connection != nil && e.Connection.BytesIn > 0 {
				closed++
			}
		}
	}
	if opened < 1 {
		t.Errorf("connection.opened events: got %d, want >= 1", opened)
	}
	if closed < 1 {
		t.Errorf("connection.closed events (with bytes): got %d, want >= 1", closed)
	}
}

// TestObserveGRPC verifies that gRPC proxy captures grpc.call.completed events
// with correct service, method, and status fields.
func TestObserveGRPC(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"temporal": rig.Temporal(),
	}, rig.WithServer(serverURL), rig.WithTimeout(120*time.Second))

	// Make a gRPC health check call through the proxy endpoint.
	ep := env.Endpoint("temporal")
	conn, err := grpc.NewClient(ep.HostPort,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("grpc health check: %v", err)
	}
	t.Logf("health check response: %v", resp.Status)

	// Give the proxy a moment to emit the event.
	time.Sleep(200 * time.Millisecond)

	// Fetch event log and verify grpc.call.completed events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type     string `json:"type"`
		GRPCCall *struct {
			Source                string          `json:"source"`
			Target                string          `json:"target"`
			Service               string          `json:"service"`
			Method                string          `json:"method"`
			GRPCStatus            string          `json:"grpc_status"`
			RequestBody           []byte          `json:"request_body,omitempty"`
			RequestBodyTruncated  bool            `json:"request_body_truncated,omitempty"`
			ResponseBody          []byte          `json:"response_body,omitempty"`
			ResponseBodyTruncated bool            `json:"response_body_truncated,omitempty"`
			RequestBodyDecoded    json.RawMessage `json:"request_body_decoded,omitempty"`
			ResponseBodyDecoded   json.RawMessage `json:"response_body_decoded,omitempty"`
		} `json:"grpc_call,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Type != "grpc.call.completed" || e.GRPCCall == nil {
			continue
		}
		g := e.GRPCCall
		t.Logf("grpc event: source=%s target=%s service=%s method=%s status=%s reqBody=%d respBody=%d reqDecoded=%s respDecoded=%s",
			g.Source, g.Target, g.Service, g.Method, g.GRPCStatus,
			len(g.RequestBody), len(g.ResponseBody),
			string(g.RequestBodyDecoded), string(g.ResponseBodyDecoded))
		if g.Service == "grpc.health.v1.Health" && g.Method == "Check" {
			found = true
			if g.GRPCStatus != "OK" {
				t.Errorf("grpc_status = %q, want OK", g.GRPCStatus)
			}
			// Raw bodies should always be captured (gRPC frames).
			if len(g.RequestBody) == 0 {
				t.Error("request_body is empty, want captured gRPC frame")
			}
			if len(g.ResponseBody) == 0 {
				t.Error("response_body is empty, want captured gRPC frame")
			}
			// Temporal dev server supports reflection, so the response
			// (which has a non-empty status field) should be decoded.
			// The request is an empty HealthCheckRequest, so its decoded
			// body is legitimately empty (zero-byte protobuf payload).
			if len(g.ResponseBodyDecoded) == 0 {
				t.Error("response_body_decoded is empty, want decoded JSON (reflection supported)")
			}
		}
	}
	if !found {
		t.Error("no grpc.call.completed event found for grpc.health.v1.Health/Check")
	}
}

// TestFuncLogWriter verifies that connect.LogWriter ships Func service logs
// to rigd's event timeline.
func TestFuncLogWriter(t *testing.T) {
	t.Parallel()
	serverURL := sharedServerURL

	env := rig.Up(t, rig.Services{
		"logger": rig.Func(func(ctx context.Context) error {
			w := connect.LogWriter(ctx)
			fmt.Fprintln(w, "hello from func")
			fmt.Fprintln(w, "second line")
			return httpx.ListenAndServe(ctx, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				rw.WriteHeader(http.StatusOK)
			}))
		}),
	}, rig.WithServer(serverURL), rig.WithTimeout(30*time.Second))

	// Hit the service to confirm it's up.
	resp, err := http.Get("http://" + env.Endpoint("logger").HostPort + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	// Give the log writer a moment to flush.
	time.Sleep(200 * time.Millisecond)

	// Fetch the event log and verify service.log events.
	logResp, err := http.Get(fmt.Sprintf("%s/environments/%s/log", serverURL, env.ID))
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer logResp.Body.Close()

	var events []struct {
		Type    string `json:"type"`
		Service string `json:"service"`
		Log     *struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		} `json:"log,omitempty"`
	}
	if err := json.NewDecoder(logResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode log: %v", err)
	}

	// Collect all log data — lines may be batched into fewer events.
	var allData string
	for _, e := range events {
		if e.Type == "service.log" && e.Service == "logger" && e.Log != nil {
			allData += e.Log.Data + "\n"
		}
	}

	if !strings.Contains(allData, "hello from func") {
		t.Errorf("log output missing 'hello from func': %s", allData)
	}
	if !strings.Contains(allData, "second line") {
		t.Errorf("log output missing 'second line': %s", allData)
	}
}

// --- helpers ---

func buildBinary(t *testing.T, srcDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(srcDir))
	cmd := exec.Command("go", "build", "-o", bin, srcDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", srcDir, err, out)
	}
	return bin
}
