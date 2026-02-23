package ready

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// GRPC checks readiness using the standard gRPC health checking protocol.
// If the service doesn't implement the health protocol (UNIMPLEMENTED),
// the check succeeds — a responding gRPC server is considered ready.
type GRPC struct{}

func (GRPC) Check(ctx context.Context, host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		// If the health service is unimplemented, the gRPC server is up.
		if status.Code(err) == codes.Unimplemented {
			return nil
		}
		return err
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("grpc health: status %s", resp.Status)
	}
	return nil
}
