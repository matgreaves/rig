package ready

import (
	"context"
	"net"
	"time"
)

// TCP checks readiness by dialing a TCP connection.
type TCP struct{}

func (TCP) Check(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
