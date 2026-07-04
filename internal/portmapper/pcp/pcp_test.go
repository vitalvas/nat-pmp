package pcp

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

var (
	testGateway = netip.MustParseAddr("192.168.1.1")
	testClient  = netip.MustParseAddr("192.168.1.50")
	testNonce   = [nonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
)

// fakeConn is a scripted packetConn.
type fakeConn struct {
	replies     []reply
	writes      [][]byte
	from        netip.AddrPort
	closed      bool
	shortWrite  bool
	writeErr    error
	readErr     error
	deadlineErr error
	afterRead   func()
}

type reply struct {
	data []byte // nil => timeout
}

func (f *fakeConn) WriteToUDPAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), b...))
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.shortWrite {
		return len(b) - 1, nil
	}
	return len(b), nil
}

func (f *fakeConn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	if f.readErr != nil {
		return 0, netip.AddrPort{}, f.readErr
	}
	if len(f.replies) == 0 {
		return 0, netip.AddrPort{}, timeoutErr{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.data == nil {
		return 0, netip.AddrPort{}, timeoutErr{}
	}
	n := copy(b, r.data)
	from := f.from
	if !from.IsValid() {
		from = netip.AddrPortFrom(testGateway, port)
	}
	if f.afterRead != nil {
		f.afterRead()
	}
	return n, from, nil
}

func (f *fakeConn) SetReadDeadline(time.Time) error { return f.deadlineErr }
func (f *fakeConn) Close() error                    { f.closed = true; return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func newTestClient(conn *fakeConn, opts ...Option) *Client {
	base := make([]Option, 0, 4+len(opts))
	base = append(base,
		WithDial(func(netip.Addr) (packetConn, netip.Addr, error) { return conn, testClient, nil }),
		WithNonce(func() ([nonceLen]byte, error) { return testNonce, nil }),
		WithClock(fixedClock()),
		WithRetry(time.Millisecond, 4),
	)
	return New(testGateway, append(base, opts...)...)
}

func mapResp(external netip.Addr, iport, eport uint16, lifetime uint32) []byte {
	return buildMapResponse(0, lifetime, testNonce, ipProtoTCP, iport, eport, external)
}

func TestExternalIP(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)}}}
		c := newTestClient(conn)
		ip, err := c.ExternalIP(context.Background())
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("203.0.113.9"), ip)
		assert.True(t, conn.closed)
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		badNonce := [nonceLen]byte{99}
		resp := buildMapResponse(0, 0, badNonce, ipProtoTCP, 0, 0, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("decode error", func(t *testing.T) {
		resp := mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)
		resp[1] = 0
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("mismatched protocol", func(t *testing.T) {
		resp := buildMapResponse(0, 0, testNonce, ipProtoUDP, 0, 0, netip.MustParseAddr("203.0.113.9"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("mismatched internal port", func(t *testing.T) {
		resp := buildMapResponse(0, 0, testNonce, ipProtoTCP, 22000, 0, netip.MustParseAddr("203.0.113.9"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("dial error", func(t *testing.T) {
		c := New(testGateway, WithDial(func(netip.Addr) (packetConn, netip.Addr, error) {
			return nil, netip.Addr{}, assert.AnError
		}))
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("nonce generation error", func(t *testing.T) {
		conn := &fakeConn{}
		c := newTestClient(conn, WithNonce(func() ([nonceLen]byte, error) {
			return [nonceLen]byte{}, assert.AnError
		}))
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("invalid client address", func(t *testing.T) {
		conn := &fakeConn{}
		c := New(testGateway,
			WithDial(func(netip.Addr) (packetConn, netip.Addr, error) { return conn, netip.Addr{}, nil }),
			WithNonce(func() ([nonceLen]byte, error) { return testNonce, nil }),
		)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("no response", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: nil}, {data: nil}, {data: nil}, {data: nil}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("ignores response from wrong source", func(t *testing.T) {
		conn := &fakeConn{
			from: netip.AddrPortFrom(netip.MustParseAddr("8.8.8.8"), port),
			replies: []reply{
				{data: mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)},
			},
		}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})
}

func TestMap(t *testing.T) {
	t.Run("success returns granted lease", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapResp(netip.MustParseAddr("203.0.113.1"), 22000, 30000, 3600)}}}
		c := newTestClient(conn)
		lease, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
			Lease:        time.Hour,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(30000), lease.ExternalPort)
		assert.Equal(t, netip.MustParseAddr("203.0.113.1"), lease.ExternalIP)
		assert.Equal(t, time.Hour, lease.Lifetime)
		assert.Equal(t, fixedClock()(), lease.Acquired)
	})

	t.Run("applies default lease", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapResp(netip.MustParseAddr("10.0.0.2"), 51820, 51820, 3600)}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 51820})
		require.NoError(t, err)
		req := conn.writes[0]
		assert.Equal(t, uint32(3600), beUint32(req[4:8]))
	})

	t.Run("binds to internal address as client address", func(t *testing.T) {
		var gotBind netip.Addr
		want := netip.MustParseAddr("192.168.1.50")
		conn := &fakeConn{replies: []reply{{data: mapResp(netip.MustParseAddr("203.0.113.1"), 22000, 30000, 3600)}}}
		c := New(testGateway,
			WithDial(func(bindAddr netip.Addr) (packetConn, netip.Addr, error) {
				gotBind = bindAddr
				return conn, bindAddr, nil
			}),
			WithNonce(func() ([nonceLen]byte, error) { return testNonce, nil }),
			WithClock(fixedClock()),
			WithRetry(time.Millisecond, 4),
		)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:        mapping.TCP,
			InternalPort:    22000,
			ExternalPort:    30000,
			InternalAddress: want,
		})
		require.NoError(t, err)
		assert.Equal(t, want, gotBind)
		// The client address is the last 16 bytes of the 24-byte MAP request header.
		req := conn.writes[0]
		clientAddr, ok := netip.AddrFromSlice(req[8:24])
		require.True(t, ok)
		assert.Equal(t, want, clientAddr.Unmap())
	})

	t.Run("invalid request", func(t *testing.T) {
		c := newTestClient(&fakeConn{})
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 0})
		require.Error(t, err)
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		resp := buildMapResponse(0, 3600, [nonceLen]byte{42}, ipProtoTCP, 1, 2, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("gateway error", func(t *testing.T) {
		resp := buildMapResponse(8, 0, testNonce, ipProtoTCP, 0, 0, netip.IPv6Unspecified())
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("mismatched protocol", func(t *testing.T) {
		resp := buildMapResponse(0, 3600, testNonce, ipProtoUDP, 22000, 30000, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("mismatched internal port", func(t *testing.T) {
		resp := buildMapResponse(0, 3600, testNonce, ipProtoTCP, 12345, 30000, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("zero external port in response", func(t *testing.T) {
		resp := buildMapResponse(0, 3600, testNonce, ipProtoTCP, 22000, 0, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("zero lifetime in response", func(t *testing.T) {
		resp := buildMapResponse(0, 0, testNonce, ipProtoTCP, 22000, 30000, netip.MustParseAddr("1.1.1.1"))
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("dial error", func(t *testing.T) {
		c := New(testGateway, WithDial(func(netip.Addr) (packetConn, netip.Addr, error) {
			return nil, netip.Addr{}, assert.AnError
		}))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("nonce error", func(t *testing.T) {
		c := newTestClient(&fakeConn{}, WithNonce(func() ([nonceLen]byte, error) {
			return [nonceLen]byte{}, assert.AnError
		}))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("invalid client address", func(t *testing.T) {
		conn := &fakeConn{}
		c := New(testGateway,
			WithDial(func(netip.Addr) (packetConn, netip.Addr, error) { return conn, netip.Addr{}, nil }),
			WithNonce(func() ([nonceLen]byte, error) { return testNonce, nil }),
		)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("exchange error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})
}

func TestUnmap(t *testing.T) {
	t.Run("success sends zero lifetime", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapResp(netip.IPv6Unspecified(), 22000, 0, 0)}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 30000})
		require.NoError(t, err)
		req := conn.writes[0]
		assert.Equal(t, uint32(0), beUint32(req[4:8]))
		assert.Equal(t, uint16(30000), beUint16(req[42:44]))
	})

	t.Run("dial error", func(t *testing.T) {
		c := New(testGateway, WithDial(func(netip.Addr) (packetConn, netip.Addr, error) {
			return nil, netip.Addr{}, assert.AnError
		}))
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("bad protocol", func(t *testing.T) {
		conn := &fakeConn{}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.Protocol("x"), InternalPort: 1})
		require.Error(t, err)
	})

	t.Run("nonce error", func(t *testing.T) {
		c := newTestClient(&fakeConn{}, WithNonce(func() ([nonceLen]byte, error) {
			return [nonceLen]byte{}, assert.AnError
		}))
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 1})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("invalid client address", func(t *testing.T) {
		conn := &fakeConn{}
		c := New(testGateway,
			WithDial(func(netip.Addr) (packetConn, netip.Addr, error) { return conn, netip.Addr{}, nil }),
			WithNonce(func() ([nonceLen]byte, error) { return testNonce, nil }),
		)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		resp := buildMapResponse(0, 0, [nonceLen]byte{42}, ipProtoTCP, 22000, 0, netip.IPv6Unspecified())
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("mismatched protocol", func(t *testing.T) {
		resp := buildMapResponse(0, 0, testNonce, ipProtoUDP, 22000, 0, netip.IPv6Unspecified())
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("mismatched internal port", func(t *testing.T) {
		resp := buildMapResponse(0, 0, testNonce, ipProtoTCP, 12345, 0, netip.IPv6Unspecified())
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("exchange error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("decode error", func(t *testing.T) {
		resp := mapResp(netip.IPv6Unspecified(), 22000, 0, 0)
		resp[1] = 0
		conn := &fakeConn{replies: []reply{{data: resp}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})
}

func TestNonceReusedUntilUnmap(t *testing.T) {
	firstNonce := [nonceLen]byte{42, 1}
	calls := 0
	conn := &fakeConn{replies: []reply{
		{data: buildMapResponse(0, 3600, firstNonce, ipProtoTCP, 22000, 30000, netip.MustParseAddr("203.0.113.1"))},
		{data: buildMapResponse(0, 0, firstNonce, ipProtoTCP, 22000, 0, netip.IPv6Unspecified())},
	}}
	c := newTestClient(conn, WithNonce(func() ([nonceLen]byte, error) {
		calls++
		return firstNonce, nil
	}))

	req := mapping.Request{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 30000, Lease: time.Hour}
	_, err := c.Map(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Unmap(context.Background(), req))

	require.Len(t, conn.writes, 2)
	assert.Equal(t, conn.writes[0][24:36], conn.writes[1][24:36])
	assert.Equal(t, 1, calls)
}

func TestExchangeErrorPaths(t *testing.T) {
	t.Run("write error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("deadline error", func(t *testing.T) {
		conn := &fakeConn{deadlineErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("short response", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: make([]byte, 40)}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("context cancelled", func(t *testing.T) {
		conn := &fakeConn{}
		c := newTestClient(conn)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.ExternalIP(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context cancelled after attempts", func(t *testing.T) {
		conn := &fakeConn{}
		c := newTestClient(conn, WithRetry(time.Millisecond, 0))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.ExternalIP(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("short write", func(t *testing.T) {
		conn := &fakeConn{shortWrite: true}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("non-timeout read error", func(t *testing.T) {
		conn := &fakeConn{readErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("too many ignored responses", func(t *testing.T) {
		replies := make([]reply, maxIgnoredResponses)
		for i := range replies {
			replies[i] = reply{data: mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)}
		}
		conn := &fakeConn{
			from:    netip.AddrPortFrom(netip.MustParseAddr("8.8.8.8"), port),
			replies: replies,
		}
		c := newTestClient(conn, WithRetry(time.Millisecond, 1))
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("context deadline is used for read deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()

		conn := &fakeConn{replies: []reply{{data: mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)}}}
		c := newTestClient(conn, WithClock(func() time.Time { return time.Now().Add(time.Hour) }))
		ip, err := c.ExternalIP(ctx)
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("203.0.113.9"), ip)
	})

	t.Run("context cancelled while reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		conn := &fakeConn{
			from:    netip.AddrPortFrom(netip.MustParseAddr("8.8.8.8"), port),
			replies: []reply{{data: mapResp(netip.MustParseAddr("203.0.113.9"), 0, 0, 0)}},
			afterRead: func() {
				cancel()
			},
		}
		c := newTestClient(conn)
		_, err := c.ExternalIP(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestRandomNonce(t *testing.T) {
	a, err := randomNonce()
	require.NoError(t, err)
	b, err := randomNonce()
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "nonces should differ")
}

func TestName(t *testing.T) {
	assert.Equal(t, ProtocolName, New(testGateway).Name())
}

func TestDefaultDial(t *testing.T) {
	c := New(testGateway)

	t.Run("without bind address derives client address", func(t *testing.T) {
		conn, clientAddr, err := c.defaultDial(netip.Addr{})
		require.NoError(t, err)
		defer conn.Close()
		assert.True(t, clientAddr.IsValid())
	})

	t.Run("binds to loopback and reports it", func(t *testing.T) {
		want := netip.MustParseAddr("127.0.0.1")
		conn, clientAddr, err := c.defaultDial(want)
		require.NoError(t, err)
		defer conn.Close()
		assert.Equal(t, want, clientAddr)
	})

	t.Run("bind to non-local address fails", func(t *testing.T) {
		_, _, err := c.defaultDial(netip.MustParseAddr("203.0.113.99"))
		require.Error(t, err)
	})
}
