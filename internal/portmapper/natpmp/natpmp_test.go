package natpmp

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

var testGateway = netip.MustParseAddr("192.168.1.1")

// fakeConn is a scripted packetConn. Each read pops the next scripted reply;
// a nil reply payload simulates a read timeout.
type fakeConn struct {
	replies     []reply
	writes      [][]byte
	from        netip.Addr
	fromPort    uint16
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
	from := f.from
	if !from.IsValid() {
		from = testGateway
	}
	fromPort := f.fromPort
	if fromPort == 0 {
		fromPort = port
	}
	n := copy(b, r.data)
	if f.afterRead != nil {
		f.afterRead()
	}
	return n, netip.AddrPortFrom(from, fromPort), nil
}

func (f *fakeConn) SetReadDeadline(time.Time) error { return f.deadlineErr }
func (f *fakeConn) Close() error                    { f.closed = true; return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// static clock for deterministic Acquired timestamps.
func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func extAddrReply(ip [4]byte) []byte {
	return []byte{0, 128, 0, 0, 0, 0, 0, 1, ip[0], ip[1], ip[2], ip[3]}
}

func mapReply(op byte, internal, external uint16, lifetime uint32) []byte {
	return []byte{
		0, op + opResponseFlag,
		0, 0,
		0, 0, 0, 1,
		byte(internal >> 8), byte(internal),
		byte(external >> 8), byte(external),
		byte(lifetime >> 24), byte(lifetime >> 16), byte(lifetime >> 8), byte(lifetime),
	}
}

func newTestClient(conn *fakeConn, opts ...Option) *Client {
	base := make([]Option, 0, 3+len(opts))
	base = append(base,
		WithDial(func() (packetConn, error) { return conn, nil }),
		WithClock(fixedClock()),
		WithRetry(time.Millisecond, 4),
	)
	return New(testGateway, append(base, opts...)...)
}

func TestExternalIP(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: extAddrReply([4]byte{203, 0, 113, 9})}}}
		c := newTestClient(conn)
		ip, err := c.ExternalIP(context.Background())
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("203.0.113.9"), ip)
		assert.True(t, conn.closed)
	})

	t.Run("retries then succeeds", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: nil}, // timeout
			{data: extAddrReply([4]byte{10, 0, 0, 1})},
		}}
		c := newTestClient(conn)
		ip, err := c.ExternalIP(context.Background())
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("10.0.0.1"), ip)
		assert.Len(t, conn.writes, 2)
	})

	t.Run("exhausts attempts", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: nil}, {data: nil}, {data: nil}, {data: nil}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("ignores response from wrong source", func(t *testing.T) {
		conn := &fakeConn{
			from: netip.MustParseAddr("8.8.8.8"),
			replies: []reply{
				{data: extAddrReply([4]byte{1, 1, 1, 1})}, // wrong source, ignored
			},
		}
		c := newTestClient(conn)
		// After ignoring, no more replies -> timeouts -> eventual failure.
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("ignores response from wrong source port", func(t *testing.T) {
		conn := &fakeConn{
			fromPort: 12345,
			replies: []reply{
				{data: extAddrReply([4]byte{1, 1, 1, 1})},
			},
		}
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
}

func TestMap(t *testing.T) {
	t.Run("success returns granted lease", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: mapReply(opMapTCP, 22000, 30000, 3600)}, // Map
			{data: extAddrReply([4]byte{203, 0, 113, 1})},  // ExternalIP
		}}
		c := newTestClient(conn)
		lease, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
			Lease:        time.Hour,
		})
		require.NoError(t, err)
		assert.Equal(t, mapping.TCP, lease.Protocol)
		assert.Equal(t, uint16(30000), lease.ExternalPort)
		assert.Equal(t, netip.MustParseAddr("203.0.113.1"), lease.ExternalIP)
		assert.Equal(t, time.Hour, lease.Lifetime)
	})

	t.Run("applies default lease when zero", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: mapReply(opMapUDP, 51820, 51820, 3600)},
			{data: extAddrReply([4]byte{10, 0, 0, 2})},
		}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.UDP,
			InternalPort: 51820,
		})
		require.NoError(t, err)
		// The written lifetime should be defaultLease (3600s = 0x00000e10 at bytes 8:12).
		req := conn.writes[0]
		assert.Equal(t, []byte{0x00, 0x00, 0x0e, 0x10}, req[8:12])
	})

	t.Run("invalid request", func(t *testing.T) {
		c := newTestClient(&fakeConn{})
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 0})
		require.Error(t, err)
	})

	t.Run("gateway error", func(t *testing.T) {
		errReply := mapReply(opMapTCP, 0, 0, 0)
		errReply[3] = 4 // out of resources
		conn := &fakeConn{replies: []reply{{data: errReply}}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.Error(t, err)
	})

	t.Run("mismatched internal port", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: mapReply(opMapTCP, 12345, 30000, 3600)},
		}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.Error(t, err)
	})

	t.Run("zero external port in response", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: mapReply(opMapTCP, 22000, 0, 3600)},
		}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.Error(t, err)
	})

	t.Run("zero lifetime in response", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{
			{data: mapReply(opMapTCP, 22000, 30000, 0)},
		}}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.Error(t, err)
	})

	t.Run("exchange error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.ErrorIs(t, err, assert.AnError)
	})
}

func TestUnmap(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapReply(opMapTCP, 22000, 0, 0)}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.NoError(t, err)
		// Unmap must send lifetime 0 and external port 0.
		req := conn.writes[0]
		assert.Equal(t, []byte{0, 0, 0, 0}, req[8:12]) // lifetime 0
		assert.Equal(t, []byte{0, 0}, req[6:8])        // external 0
	})

	t.Run("bad protocol", func(t *testing.T) {
		c := newTestClient(&fakeConn{})
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.Protocol("x"), InternalPort: 1})
		require.Error(t, err)
	})

	t.Run("exchange error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("decode error", func(t *testing.T) {
		bad := mapReply(opMapTCP, 22000, 0, 0)
		bad[1] = 200
		conn := &fakeConn{replies: []reply{{data: bad}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})

	t.Run("mismatched internal port", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: mapReply(opMapTCP, 12345, 0, 0)}}}
		c := newTestClient(conn)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})
}

func TestDialError(t *testing.T) {
	c := New(testGateway, WithDial(func() (packetConn, error) {
		return nil, assert.AnError
	}))
	_, err := c.ExternalIP(context.Background())
	require.Error(t, err)
}

func TestExchangeErrorPaths(t *testing.T) {
	t.Run("write error", func(t *testing.T) {
		conn := &fakeConn{writeErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("set deadline error", func(t *testing.T) {
		conn := &fakeConn{deadlineErr: assert.AnError}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
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
			replies[i] = reply{data: extAddrReply([4]byte{1, 1, 1, 1})}
		}
		conn := &fakeConn{
			from:    netip.MustParseAddr("8.8.8.8"),
			replies: replies,
		}
		c := newTestClient(conn, WithRetry(time.Millisecond, 1))
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("context deadline is used for read deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()

		conn := &fakeConn{replies: []reply{{data: extAddrReply([4]byte{203, 0, 113, 7})}}}
		c := newTestClient(conn, WithClock(func() time.Time { return time.Now().Add(time.Hour) }))
		ip, err := c.ExternalIP(ctx)
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("203.0.113.7"), ip)
	})

	t.Run("context cancelled while reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		conn := &fakeConn{
			from:    netip.MustParseAddr("8.8.8.8"),
			replies: []reply{{data: extAddrReply([4]byte{1, 1, 1, 1})}},
			afterRead: func() {
				cancel()
			},
		}
		c := newTestClient(conn)
		_, err := c.ExternalIP(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("short response", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: []byte{0, 128, 0}}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("non-timeout read error surfaces", func(t *testing.T) {
		conn := &fakeConn{replies: []reply{{data: []byte{}}}} // empty (non-nil) => copy 0, returns as valid short read
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("decode error after read", func(t *testing.T) {
		// 12 bytes but wrong opcode -> passes length check, fails decode.
		bad := []byte{0, 200, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
		conn := &fakeConn{replies: []reply{{data: bad}}}
		c := newTestClient(conn)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})
}

func TestMapExternalIPFailure(t *testing.T) {
	// Map succeeds but the follow-up ExternalIP call gets no response.
	conn := &fakeConn{replies: []reply{
		{data: mapReply(opMapTCP, 22000, 30000, 3600)},
		{data: nil},
		{data: nil},
		{data: nil},
		{data: nil},
	}}
	c := newTestClient(conn)
	_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
	require.Error(t, err)
}
