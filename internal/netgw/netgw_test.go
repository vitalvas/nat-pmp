package netgw

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleRoute mirrors the format of /proc/net/route with a default route via
// 192.168.1.1 and a couple of non-default entries.
const sampleRoute = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
eth0	0001A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
lo	0000007F	00000000	0001	0	0	0	000000FF	0	0	0
`

func TestParseDefaultGateway(t *testing.T) {
	t.Run("finds default route", func(t *testing.T) {
		addr, err := parseDefaultGateway(strings.NewReader(sampleRoute))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("no default route", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	0001A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
`
		_, err := parseDefaultGateway(strings.NewReader(route))
		require.Error(t, err)
	})

	t.Run("empty input", func(t *testing.T) {
		_, err := parseDefaultGateway(strings.NewReader(""))
		require.Error(t, err)
	})

	t.Run("header only", func(t *testing.T) {
		_, err := parseDefaultGateway(strings.NewReader("Iface\tDestination\tGateway\n"))
		require.Error(t, err)
	})

	t.Run("skips short lines", func(t *testing.T) {
		route := "Iface\tDestination\tGateway\nshort line\neth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("invalid hex gateway", func(t *testing.T) {
		route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\neth0\t00000000\tZZZZZZZZ\t0003\t0\t0\t100\t00000000\n"
		_, err := parseDefaultGateway(strings.NewReader(route))
		require.Error(t, err)
	})

	t.Run("invalid flags", func(t *testing.T) {
		route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\neth0\t00000000\t0101A8C0\tzzzz\t0\t0\t100\t00000000\n"
		_, err := parseDefaultGateway(strings.NewReader(route))
		require.Error(t, err)
	})

	t.Run("skips default destination with non-zero mask", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0201A8C0	0003	0	0	100	00FFFFFF	0	0	0
eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
`
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("skips route without gateway flag", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0201A8C0	0001	0	0	100	00000000	0	0	0
eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
`
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("selects lowest metric default route", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0201A8C0	0003	0	0	200	00000000	0	0	0
wlan0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
`
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("skips down gateway route", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0201A8C0	0002	0	0	50	00000000	0	0	0
eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
`
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("skips unspecified gateway", func(t *testing.T) {
		route := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	00000000	0003	0	0	50	00000000	0	0	0
eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
`
		addr, err := parseDefaultGateway(strings.NewReader(route))
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("invalid metric errors", func(t *testing.T) {
		route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\neth0\t00000000\t0101A8C0\t0003\t0\t0\tbad\t00000000\n"
		_, err := parseDefaultGateway(strings.NewReader(route))
		require.Error(t, err)
	})

	t.Run("read error before header", func(t *testing.T) {
		_, err := parseDefaultGateway(&errorAfterReader{})
		require.Error(t, err)
	})

	t.Run("read error after header", func(t *testing.T) {
		r := &errorAfterReader{data: []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n")}
		_, err := parseDefaultGateway(r)
		require.Error(t, err)
	})
}

type errorAfterReader struct {
	data []byte
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, assert.AnError
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestParseHexAddr(t *testing.T) {
	tests := []struct {
		name    string
		hex     string
		want    string
		wantErr bool
	}{
		{name: "gateway", hex: "0101A8C0", want: "192.168.1.1"},
		{name: "high octet", hex: "FE01A8C0", want: "192.168.1.254"},
		{name: "zero", hex: "00000000", want: "0.0.0.0"},
		{name: "invalid", hex: "nothex", wantErr: true},
		{name: "too short", hex: "1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := parseHexAddr(tt.hex)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, netip.MustParseAddr(tt.want), addr)
		})
	}
}

func TestDefault(t *testing.T) {
	t.Run("reads gateway from fixture", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "route")
		require.NoError(t, os.WriteFile(path, []byte(sampleRoute), 0o600))

		orig := routePath
		routePath = path
		t.Cleanup(func() { routePath = orig })

		addr, err := Default()
		require.NoError(t, err)
		assert.Equal(t, netip.MustParseAddr("192.168.1.1"), addr)
	})

	t.Run("missing file errors", func(t *testing.T) {
		orig := routePath
		routePath = filepath.Join(t.TempDir(), "does-not-exist")
		t.Cleanup(func() { routePath = orig })

		_, err := Default()
		require.Error(t, err)
	})
}
