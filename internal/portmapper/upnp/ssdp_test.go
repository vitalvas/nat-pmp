package upnp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLocation(t *testing.T) {
	t.Run("extracts location case-insensitively", func(t *testing.T) {
		datagram := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLOCATION: http://192.168.1.1:5000/desc.xml\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n"
		loc, err := parseLocation([]byte(datagram))
		require.NoError(t, err)
		assert.Equal(t, "http://192.168.1.1:5000/desc.xml", loc)
	})

	t.Run("no location header", func(t *testing.T) {
		datagram := "HTTP/1.1 200 OK\r\nST: foo\r\n\r\n"
		loc, err := parseLocation([]byte(datagram))
		require.NoError(t, err)
		assert.Empty(t, loc)
	})

	t.Run("ignores malformed header lines", func(t *testing.T) {
		datagram := "HTTP/1.1 200 OK\r\ngarbage-no-colon\r\nLOCATION: http://x/d\r\n\r\n"
		loc, err := parseLocation([]byte(datagram))
		require.NoError(t, err)
		assert.Equal(t, "http://x/d", loc)
	})

	t.Run("empty datagram", func(t *testing.T) {
		_, err := parseLocation(nil)
		require.Error(t, err)
	})

	t.Run("eof while reading headers", func(t *testing.T) {
		loc, err := parseLocation([]byte("HTTP/1.1 200 OK\r\n"))
		require.NoError(t, err)
		assert.Empty(t, loc)
	})
}

func TestBuildSearchRequest(t *testing.T) {
	t.Run("contains required headers", func(t *testing.T) {
		msg := string(buildSearchRequest(2 * time.Second))
		assert.Contains(t, msg, "M-SEARCH * HTTP/1.1\r\n")
		assert.Contains(t, msg, "HOST: 239.255.255.250:1900\r\n")
		assert.Contains(t, msg, `MAN: "ssdp:discover"`)
		assert.Contains(t, msg, "MX: 2\r\n")
		assert.Contains(t, msg, fmt.Sprintf("ST: %s", searchType))
	})

	t.Run("mx floored at 1", func(t *testing.T) {
		msg := string(buildSearchRequest(100 * time.Millisecond))
		assert.Contains(t, msg, "MX: 1\r\n")
	})
}

func TestSplitHeader(t *testing.T) {
	tests := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{line: "Location: http://x", wantKey: "Location", wantValue: "http://x", wantOK: true},
		{line: "ST:\tfoo", wantKey: "ST", wantValue: "foo", wantOK: true},
		{line: "nocolon", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			k, v, ok := splitHeader(tt.line)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantKey, k)
				assert.Equal(t, tt.wantValue, v)
			}
		})
	}
}

func TestDiscoverContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discover(ctx, time.Second)
	require.Error(t, err)
}

func TestDefaultSearchUsesDiscover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().search(ctx)
	require.Error(t, err)
}

// fakeReader scripts datagrams for collectResults; a nil entry ends the window.
type fakeReader struct {
	datagrams [][]byte
}

func (f *fakeReader) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	if len(f.datagrams) == 0 {
		return 0, nil, timeoutErr{}
	}
	d := f.datagrams[0]
	f.datagrams = f.datagrams[1:]
	if d == nil {
		return 0, nil, timeoutErr{}
	}
	n := copy(b, d)
	return n, &net.UDPAddr{}, nil
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type fakeSSDPConn struct {
	datagrams   [][]byte
	writes      [][]byte
	closed      bool
	shortWrite  bool
	writeErr    error
	deadlineErr error
	deadline    time.Time
}

func (f *fakeSSDPConn) WriteToUDP(b []byte, _ *net.UDPAddr) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), b...))
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.shortWrite {
		return len(b) - 1, nil
	}
	return len(b), nil
}

func (f *fakeSSDPConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	if len(f.datagrams) == 0 {
		return 0, nil, timeoutErr{}
	}
	d := f.datagrams[0]
	f.datagrams = f.datagrams[1:]
	if d == nil {
		return 0, nil, timeoutErr{}
	}
	n := copy(b, d)
	return n, &net.UDPAddr{}, nil
}

func (f *fakeSSDPConn) SetReadDeadline(deadline time.Time) error {
	f.deadline = deadline
	return f.deadlineErr
}

func (f *fakeSSDPConn) Close() error {
	f.closed = true
	return nil
}

func resp(loc string) []byte {
	return fmt.Appendf(nil, "HTTP/1.1 200 OK\r\nLOCATION: %s\r\n\r\n", loc)
}

func TestDiscover(t *testing.T) {
	t.Run("success uses context deadline and closes socket", func(t *testing.T) {
		conn := &fakeSSDPConn{datagrams: [][]byte{resp("http://192.168.1.1/d"), nil}}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
		defer cancel()
		wantDeadline, ok := ctx.Deadline()
		require.True(t, ok)

		results, err := discoverWithConn(ctx, time.Hour, conn)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "http://192.168.1.1/d", results[0].Location)
		assert.Len(t, conn.writes, 1)
		assert.Equal(t, wantDeadline, conn.deadline)
	})

	t.Run("write error", func(t *testing.T) {
		conn := &fakeSSDPConn{writeErr: assert.AnError}
		_, err := discoverWithConn(context.Background(), time.Second, conn)
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("short write", func(t *testing.T) {
		conn := &fakeSSDPConn{shortWrite: true}
		_, err := discoverWithConn(context.Background(), time.Second, conn)
		require.Error(t, err)
	})

	t.Run("deadline error", func(t *testing.T) {
		conn := &fakeSSDPConn{deadlineErr: assert.AnError}
		_, err := discoverWithConn(context.Background(), time.Second, conn)
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("no responses", func(t *testing.T) {
		conn := &fakeSSDPConn{datagrams: [][]byte{nil}}
		_, err := discoverWithConn(context.Background(), time.Second, conn)
		require.Error(t, err)
	})
}

func TestCollectResults(t *testing.T) {
	t.Run("dedupes locations", func(t *testing.T) {
		r := &fakeReader{datagrams: [][]byte{
			resp("http://192.168.1.1/d"),
			resp("http://192.168.1.1/d"), // duplicate
			resp("http://192.168.1.2/d"),
			nil, // timeout ends window
		}}
		results, err := collectResults(context.Background(), r)
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, "http://192.168.1.1/d", results[0].Location)
		assert.Equal(t, "http://192.168.1.2/d", results[1].Location)
	})

	t.Run("skips responses without location", func(t *testing.T) {
		r := &fakeReader{datagrams: [][]byte{
			[]byte("HTTP/1.1 200 OK\r\nST: foo\r\n\r\n"),
			resp("http://x/d"),
			nil,
		}}
		results, err := collectResults(context.Background(), r)
		require.NoError(t, err)
		require.Len(t, results, 1)
	})

	t.Run("no responses is an error", func(t *testing.T) {
		r := &fakeReader{datagrams: [][]byte{nil}}
		_, err := collectResults(context.Background(), r)
		require.Error(t, err)
	})

	t.Run("context cancelled with partial results returns them", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := &fakeReader{datagrams: [][]byte{resp("http://x/d")}}
		// Read one, then cancel before the next iteration.
		results, err := collectResultsWithHook(ctx, r, cancel)
		require.NoError(t, err)
		require.Len(t, results, 1)
	})

	t.Run("context cancelled with no results errors", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &fakeReader{datagrams: [][]byte{resp("http://x/d")}}
		_, err := collectResults(ctx, r)
		require.Error(t, err)
	})

	t.Run("non-timeout read error surfaces", func(t *testing.T) {
		r := errReader{err: assert.AnError}
		_, err := collectResults(context.Background(), r)
		require.Error(t, err)
		assert.True(t, errors.Is(err, assert.AnError))
	})
}

// collectResultsWithHook cancels the context after the first datagram to
// exercise the "partial results on cancellation" branch deterministically.
func collectResultsWithHook(ctx context.Context, r *fakeReader, cancel context.CancelFunc) ([]ssdpResult, error) {
	// Wrap the reader so it cancels after yielding its first datagram.
	hooked := &hookReader{inner: r, after: cancel}
	return collectResults(ctx, hooked)
}

type hookReader struct {
	inner *fakeReader
	after func()
	fired bool
}

func (h *hookReader) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	n, addr, err := h.inner.ReadFromUDP(b)
	if !h.fired {
		h.fired = true
		h.after()
	}
	return n, addr, err
}

type errReader struct {
	err error
}

func (e errReader) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	return 0, nil, e.err
}
