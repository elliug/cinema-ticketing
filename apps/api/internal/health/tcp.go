package health

import (
	"context"
	"net"
)

// TCPProbe verifies that a dependency accepts TCP connections. Protocol-level
// checks can replace it when database and Redis clients are introduced.
type TCPProbe struct {
	id      string
	address string
	dialer  net.Dialer
}

// NewTCPProbe creates a dependency probe for a host:port address.
func NewTCPProbe(id, address string) TCPProbe {
	return TCPProbe{id: id, address: address}
}

// Name implements Probe.
func (p TCPProbe) Name() string { return p.id }

// Check implements Probe.
func (p TCPProbe) Check(ctx context.Context) Result {
	connection, err := p.dialer.DialContext(ctx, "tcp", p.address)
	if err != nil {
		return Unavailable("connection failed")
	}
	_ = connection.Close()
	return OK()
}
