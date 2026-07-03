package natpmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// ProtocolName identifies this protocol in logs and detection ordering.
const ProtocolName = "natpmp"

// port is the well-known NAT-PMP server port on the gateway.
const port = 5351

// defaultLease is applied when a request omits a lifetime.
const defaultLease = time.Hour

// Retransmission parameters (RFC 6886 section 3.1): start at 250ms and double
// on each retry. The default of 4 attempts keeps detection responsive while
// still tolerating a couple of lost datagrams.
const (
	defaultInitialTimeout = 250 * time.Millisecond
	defaultMaxAttempts    = 4
	maxIgnoredResponses   = 16
)

// packetConn is the subset of *net.UDPConn used by the client, abstracted so
// tests can inject a fake transport.
type packetConn interface {
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
	ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// dialFunc opens a packetConn bound for communicating with the gateway.
type dialFunc func() (packetConn, error)

// Client speaks NAT-PMP to a single gateway.
type Client struct {
	gateway        netip.Addr
	dial           dialFunc
	initialTimeout time.Duration
	maxAttempts    int
	now            func() time.Time
}

// Option customizes a Client.
type Option func(*Client)

// WithDial overrides the transport dial function. Used in tests.
func WithDial(d func() (packetConn, error)) Option {
	return func(c *Client) { c.dial = dialFunc(d) }
}

// WithRetry overrides the retransmission parameters.
func WithRetry(initialTimeout time.Duration, maxAttempts int) Option {
	return func(c *Client) {
		c.initialTimeout = initialTimeout
		c.maxAttempts = maxAttempts
	}
}

// WithClock overrides the time source. Used in tests.
func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// New creates a NAT-PMP client targeting the given gateway address.
func New(gateway netip.Addr, opts ...Option) *Client {
	c := &Client{
		gateway:        gateway,
		initialTimeout: defaultInitialTimeout,
		maxAttempts:    defaultMaxAttempts,
		now:            time.Now,
	}
	c.dial = c.defaultDial
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies the protocol.
func (c *Client) Name() string { return ProtocolName }

func (c *Client) defaultDial() (packetConn, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *Client) serverAddr() netip.AddrPort {
	return netip.AddrPortFrom(c.gateway, port)
}

// exchange sends the request and returns the first response of the expected
// length, retransmitting with exponential backoff until the context is done or
// the attempt budget is exhausted.
func (c *Client) exchange(ctx context.Context, req []byte, respLen int) ([]byte, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, fmt.Errorf("natpmp: dial: %w", err)
	}
	defer conn.Close()

	buf := make([]byte, 16)
	timeout := c.initialTimeout

	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := conn.WriteToUDPAddrPort(req, c.serverAddr())
		if err != nil {
			return nil, fmt.Errorf("natpmp: write: %w", err)
		}
		if n != len(req) {
			return nil, fmt.Errorf("natpmp: short write: %d of %d bytes", n, len(req))
		}

		deadline := c.now().Add(timeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, fmt.Errorf("natpmp: set deadline: %w", err)
		}

		ignored := 0
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, from, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				if isTimeout(err) {
					timeout *= 2
					break
				}
				return nil, fmt.Errorf("natpmp: read: %w", err)
			}
			if from != c.serverAddr() {
				ignored++
				if ignored >= maxIgnoredResponses {
					timeout *= 2
					break
				}
				continue
			}
			if n < respLen {
				return nil, fmt.Errorf("natpmp: short response: %d bytes", n)
			}
			out := make([]byte, n)
			copy(out, buf[:n])
			return out, nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("natpmp: no response after %d attempts", c.maxAttempts)
}

func isTimeout(err error) bool {
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// ExternalIP returns the gateway's WAN address.
func (c *Client) ExternalIP(ctx context.Context) (netip.Addr, error) {
	resp, err := c.exchange(ctx, encodeExternalAddressRequest(), 12)
	if err != nil {
		return netip.Addr{}, err
	}
	decoded, err := decodeExternalAddressResponse(resp)
	if err != nil {
		return netip.Addr{}, err
	}
	return netip.AddrFrom4(decoded.ExternalIP), nil
}

// Map creates or refreshes a mapping and returns the granted lease.
func (c *Client) Map(ctx context.Context, r mapping.Request) (mapping.Lease, error) {
	if err := r.Validate(); err != nil {
		return mapping.Lease{}, err
	}
	lifetime := r.Lease
	if lifetime <= 0 {
		lifetime = defaultLease
	}

	req, _ := encodeMapRequest(r.Protocol, r.InternalPort, r.ExternalPort, uint32(lifetime/time.Second))
	resp, err := c.exchange(ctx, req, 16)
	if err != nil {
		return mapping.Lease{}, err
	}
	decoded, err := decodeMapResponse(resp, r.Protocol)
	if err != nil {
		return mapping.Lease{}, err
	}
	if decoded.InternalPort != r.InternalPort {
		return mapping.Lease{}, fmt.Errorf("natpmp: response internal port %d does not match request %d", decoded.InternalPort, r.InternalPort)
	}
	if decoded.ExternalPort == 0 {
		return mapping.Lease{}, fmt.Errorf("natpmp: response external port must be non-zero")
	}
	if decoded.LifetimeSec == 0 {
		return mapping.Lease{}, fmt.Errorf("natpmp: response lifetime must be positive")
	}

	extIP, err := c.ExternalIP(ctx)
	if err != nil {
		return mapping.Lease{}, err
	}

	return mapping.Lease{
		Protocol:     r.Protocol,
		InternalPort: decoded.InternalPort,
		ExternalPort: decoded.ExternalPort,
		ExternalIP:   extIP,
		Lifetime:     time.Duration(decoded.LifetimeSec) * time.Second,
		Acquired:     c.now(),
	}, nil
}

// Unmap releases a mapping by requesting it with a zero lifetime and zero
// external port, as specified by RFC 6886 section 3.4.
func (c *Client) Unmap(ctx context.Context, r mapping.Request) error {
	if err := r.Validate(); err != nil {
		return err
	}

	req, _ := encodeMapRequest(r.Protocol, r.InternalPort, 0, 0)
	resp, err := c.exchange(ctx, req, 16)
	if err != nil {
		return err
	}
	decoded, err := decodeMapResponse(resp, r.Protocol)
	if err != nil {
		return err
	}
	if decoded.InternalPort != r.InternalPort {
		return fmt.Errorf("natpmp: response internal port %d does not match request %d", decoded.InternalPort, r.InternalPort)
	}
	return nil
}
