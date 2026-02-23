package ready

import (
	"context"
	"fmt"
	"net"
	"time"
)

// TCP checks readiness by dialing a TCP connection.
type TCP struct{}

func (TCP) Check(ctx context.Context, host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
