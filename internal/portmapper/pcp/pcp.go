package pcp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// ProtocolName identifies this protocol in logs and detection ordering.
const ProtocolName = "pcp"

// port is the well-known PCP server port on the gateway.
const port = 5351

// defaultLease is applied when a request omits a lifetime.
const defaultLease = time.Hour

// Retransmission parameters mirror RFC 6887 section 8.1.1 in spirit: start at
// 250ms and double on each retry.
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

// dialFunc opens a packetConn and reports the local address the gateway will
// see, which PCP requires as the client address in the request header.
type dialFunc func() (conn packetConn, clientAddr netip.Addr, err error)

// nonceFunc fills a fresh MAP nonce.
type nonceFunc func() ([nonceLen]byte, error)

// Client speaks PCP to a single gateway.
type Client struct {
	gateway        netip.Addr
	dial           dialFunc
	nonce          nonceFunc
	initialTimeout time.Duration
	maxAttempts    int
	now            func() time.Time

	mu     sync.Mutex
	nonces map[nonceKey][nonceLen]byte
}

type nonceKey struct {
	protocol     mapping.Protocol
	internalPort uint16
}

// Option customizes a Client.
type Option func(*Client)

// WithDial overrides the transport dial function. Used in tests.
func WithDial(d func() (packetConn, netip.Addr, error)) Option {
	return func(c *Client) { c.dial = dialFunc(d) }
}

// WithNonce overrides the nonce generator. Used in tests.
func WithNonce(n func() ([nonceLen]byte, error)) Option {
	return func(c *Client) { c.nonce = nonceFunc(n) }
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

// New creates a PCP client targeting the given gateway address.
func New(gateway netip.Addr, opts ...Option) *Client {
	c := &Client{
		gateway:        gateway,
		nonce:          randomNonce,
		initialTimeout: defaultInitialTimeout,
		maxAttempts:    defaultMaxAttempts,
		now:            time.Now,
		nonces:         make(map[nonceKey][nonceLen]byte),
	}
	c.dial = c.defaultDial
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies the protocol.
func (c *Client) Name() string { return ProtocolName }

func randomNonce() ([nonceLen]byte, error) {
	var n [nonceLen]byte
	if _, err := rand.Read(n[:]); err != nil {
		return n, err
	}
	return n, nil
}

// defaultDial opens an unconnected UDP socket for talking to the gateway and
// reports the local address the gateway will see. A *net.UDPConn satisfies the
// packetConn interface directly, so no adapter is needed.
func (c *Client) defaultDial() (packetConn, netip.Addr, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	local, err := c.localAddr()
	if err != nil {
		conn.Close()
		return nil, netip.Addr{}, err
	}
	return conn, local, nil
}

// localAddr determines this host's source address on the route to the gateway
// by opening a throwaway connected socket; no packets are sent.
func (c *Client) localAddr() (netip.Addr, error) {
	probe, err := net.Dial("udp4", c.serverAddr().String())
	if err != nil {
		return netip.Addr{}, err
	}
	defer probe.Close()
	udpAddr, ok := probe.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("pcp: cannot determine local address")
	}
	local, ok := netip.AddrFromSlice(udpAddr.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("pcp: cannot determine local address")
	}
	return local.Unmap(), nil
}

func (c *Client) serverAddr() netip.AddrPort {
	return netip.AddrPortFrom(c.gateway, port)
}

func (c *Client) nonceFor(r mapping.Request) ([nonceLen]byte, error) {
	k := nonceKey{protocol: r.Protocol, internalPort: r.InternalPort}

	c.mu.Lock()
	defer c.mu.Unlock()

	if nonce, ok := c.nonces[k]; ok {
		return nonce, nil
	}
	nonce, err := c.nonce()
	if err != nil {
		return [nonceLen]byte{}, err
	}
	c.nonces[k] = nonce
	return nonce, nil
}

func (c *Client) deleteNonce(r mapping.Request) {
	k := nonceKey{protocol: r.Protocol, internalPort: r.InternalPort}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nonces, k)
}

// exchangeOn sends the request over the given connection and returns the first
// response of at least the expected length, retransmitting with exponential
// backoff.
func (c *Client) exchangeOn(ctx context.Context, conn packetConn, req []byte) ([]byte, error) {
	buf := make([]byte, 1100)
	timeout := c.initialTimeout

	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := conn.WriteToUDPAddrPort(req, c.serverAddr())
		if err != nil {
			return nil, fmt.Errorf("pcp: write: %w", err)
		}
		if n != len(req) {
			return nil, fmt.Errorf("pcp: short write: %d of %d bytes", n, len(req))
		}

		deadline := c.now().Add(timeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, fmt.Errorf("pcp: set deadline: %w", err)
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
				return nil, fmt.Errorf("pcp: read: %w", err)
			}
			if from != c.serverAddr() {
				ignored++
				if ignored >= maxIgnoredResponses {
					timeout *= 2
					break
				}
				continue
			}
			if n < responseLen {
				return nil, fmt.Errorf("pcp: short response: %d bytes", n)
			}
			out := make([]byte, n)
			copy(out, buf[:n])
			return out, nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("pcp: no response after %d attempts", c.maxAttempts)
}

func isTimeout(err error) bool {
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// ExternalIP probes the gateway with a short-lived MAP request and returns the
// external address the gateway reports. PCP has no dedicated "get external
// address" opcode, so a MAP with a zero internal port serves as the probe.
func (c *Client) ExternalIP(ctx context.Context) (netip.Addr, error) {
	conn, clientAddr, err := c.dial()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("pcp: dial: %w", err)
	}
	defer conn.Close()

	nonce, err := c.nonce()
	if err != nil {
		return netip.Addr{}, err
	}
	req, err := encodeMapRequest(mapRequest{
		Nonce:        nonce,
		Protocol:     mapping.TCP,
		InternalPort: 0,
		ExternalPort: 0,
		ExternalAddr: netip.IPv6Unspecified(),
		ClientAddr:   clientAddr,
		LifetimeSec:  0,
	})
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := c.exchangeOn(ctx, conn, req)
	if err != nil {
		return netip.Addr{}, err
	}
	decoded, err := decodeMapResponse(resp)
	if err != nil {
		return netip.Addr{}, err
	}
	if decoded.Nonce != nonce {
		return netip.Addr{}, fmt.Errorf("pcp: nonce mismatch in response")
	}
	if decoded.Protocol != ipProtoTCP {
		return netip.Addr{}, fmt.Errorf("pcp: response protocol %d does not match request %d", decoded.Protocol, ipProtoTCP)
	}
	if decoded.InternalPort != 0 {
		return netip.Addr{}, fmt.Errorf("pcp: response internal port %d does not match request 0", decoded.InternalPort)
	}
	return decoded.ExternalAddr, nil
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

	conn, clientAddr, err := c.dial()
	if err != nil {
		return mapping.Lease{}, fmt.Errorf("pcp: dial: %w", err)
	}
	defer conn.Close()

	nonce, err := c.nonceFor(r)
	if err != nil {
		return mapping.Lease{}, err
	}
	req, err := encodeMapRequest(mapRequest{
		Nonce:        nonce,
		Protocol:     r.Protocol,
		InternalPort: r.InternalPort,
		ExternalPort: r.ExternalPort,
		ExternalAddr: netip.IPv6Unspecified(),
		ClientAddr:   clientAddr,
		LifetimeSec:  uint32(lifetime / time.Second),
	})
	if err != nil {
		return mapping.Lease{}, err
	}
	resp, err := c.exchangeOn(ctx, conn, req)
	if err != nil {
		return mapping.Lease{}, err
	}
	decoded, err := decodeMapResponse(resp)
	if err != nil {
		return mapping.Lease{}, err
	}
	if decoded.Nonce != nonce {
		return mapping.Lease{}, fmt.Errorf("pcp: nonce mismatch in response")
	}
	proto, _ := ipProtocol(r.Protocol)
	if decoded.Protocol != proto {
		return mapping.Lease{}, fmt.Errorf("pcp: response protocol %d does not match request %d", decoded.Protocol, proto)
	}
	if decoded.InternalPort != r.InternalPort {
		return mapping.Lease{}, fmt.Errorf("pcp: response internal port %d does not match request %d", decoded.InternalPort, r.InternalPort)
	}
	if decoded.ExternalPort == 0 {
		return mapping.Lease{}, fmt.Errorf("pcp: response external port must be non-zero")
	}
	if decoded.LifetimeSec == 0 {
		return mapping.Lease{}, fmt.Errorf("pcp: response lifetime must be positive")
	}

	return mapping.Lease{
		Protocol:     r.Protocol,
		InternalPort: decoded.InternalPort,
		ExternalPort: decoded.ExternalPort,
		ExternalIP:   decoded.ExternalAddr,
		Lifetime:     time.Duration(decoded.LifetimeSec) * time.Second,
		Acquired:     c.now(),
	}, nil
}

// Unmap releases a mapping by issuing a MAP request with a zero lifetime, as
// specified by RFC 6887 section 15.
func (c *Client) Unmap(ctx context.Context, r mapping.Request) error {
	if err := r.Validate(); err != nil {
		return err
	}

	conn, clientAddr, err := c.dial()
	if err != nil {
		return fmt.Errorf("pcp: dial: %w", err)
	}
	defer conn.Close()

	nonce, err := c.nonceFor(r)
	if err != nil {
		return err
	}
	req, err := encodeMapRequest(mapRequest{
		Nonce:        nonce,
		Protocol:     r.Protocol,
		InternalPort: r.InternalPort,
		ExternalPort: r.ExternalPort,
		ExternalAddr: netip.IPv6Unspecified(),
		ClientAddr:   clientAddr,
		LifetimeSec:  0,
	})
	if err != nil {
		return err
	}
	resp, err := c.exchangeOn(ctx, conn, req)
	if err != nil {
		return err
	}
	decoded, err := decodeMapResponse(resp)
	if err != nil {
		return err
	}
	if decoded.Nonce != nonce {
		return fmt.Errorf("pcp: nonce mismatch in response")
	}
	proto, _ := ipProtocol(r.Protocol)
	if decoded.Protocol != proto {
		return fmt.Errorf("pcp: response protocol %d does not match request %d", decoded.Protocol, proto)
	}
	if decoded.InternalPort != r.InternalPort {
		return fmt.Errorf("pcp: response internal port %d does not match request %d", decoded.InternalPort, r.InternalPort)
	}
	c.deleteNonce(r)
	return nil
}
