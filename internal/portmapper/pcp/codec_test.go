package pcp

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

func TestResultError(t *testing.T) {
	tests := []struct {
		name string
		code uint8
		want error
	}{
		{name: "success", code: 0, want: nil},
		{name: "unsupported version", code: 1, want: portmapper.ErrUnsupported},
		{name: "not authorized", code: 2, want: portmapper.ErrNotAuthorized},
		{name: "unsupported opcode", code: 4, want: portmapper.ErrUnsupported},
		{name: "no resources", code: 8, want: portmapper.ErrNoResources},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, resultError(tt.code), tt.want)
		})
	}

	t.Run("malformed is generic", func(t *testing.T) {
		err := resultError(3)
		require.Error(t, err)
		assert.NotErrorIs(t, err, portmapper.ErrUnsupported)
	})
}

func TestIPProtocol(t *testing.T) {
	tcp, err := ipProtocol(mapping.TCP)
	require.NoError(t, err)
	assert.Equal(t, uint8(6), tcp)

	udp, err := ipProtocol(mapping.UDP)
	require.NoError(t, err)
	assert.Equal(t, uint8(17), udp)

	_, err = ipProtocol(mapping.Protocol("x"))
	require.Error(t, err)
}

func TestEncodeMapRequest(t *testing.T) {
	nonce := [nonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

	t.Run("tcp with ipv4 client", func(t *testing.T) {
		b, err := encodeMapRequest(mapRequest{
			Nonce:        nonce,
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
			ExternalAddr: netip.IPv6Unspecified(),
			ClientAddr:   netip.MustParseAddr("192.168.1.50"),
			LifetimeSec:  3600,
		})
		require.NoError(t, err)
		require.Len(t, b, requestLen)

		assert.Equal(t, byte(protoVersion), b[0])
		assert.Equal(t, byte(opMap), b[1])
		assert.Equal(t, uint32(3600), beUint32(b[4:8]))
		// Client address v4-mapped: last 4 bytes are the v4 octets.
		assert.Equal(t, []byte{192, 168, 1, 50}, b[20:24])
		// Nonce.
		assert.Equal(t, nonce[:], b[24:36])
		assert.Equal(t, byte(ipProtoTCP), b[36])
		assert.Equal(t, uint16(22000), beUint16(b[40:42]))
		assert.Equal(t, uint16(30000), beUint16(b[42:44]))
	})

	t.Run("bad protocol", func(t *testing.T) {
		_, err := encodeMapRequest(mapRequest{Protocol: mapping.Protocol("x")})
		require.Error(t, err)
	})

	t.Run("invalid client address", func(t *testing.T) {
		_, err := encodeMapRequest(mapRequest{
			Protocol:     mapping.TCP,
			ExternalAddr: netip.IPv6Unspecified(),
		})
		require.Error(t, err)
	})

	t.Run("invalid external address", func(t *testing.T) {
		_, err := encodeMapRequest(mapRequest{
			Protocol:   mapping.TCP,
			ClientAddr: netip.MustParseAddr("192.168.1.50"),
		})
		require.Error(t, err)
	})
}

func TestDecodeMapResponse(t *testing.T) {
	nonce := [nonceLen]byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 15, 14}
	valid := buildMapResponse(0, 3600, nonce, ipProtoTCP, 22000, 30000, netip.MustParseAddr("203.0.113.7"))

	t.Run("success v4-mapped external", func(t *testing.T) {
		resp, err := decodeMapResponse(valid)
		require.NoError(t, err)
		assert.Equal(t, nonce, resp.Nonce)
		assert.Equal(t, uint16(22000), resp.InternalPort)
		assert.Equal(t, uint16(30000), resp.ExternalPort)
		assert.Equal(t, uint32(3600), resp.LifetimeSec)
		assert.Equal(t, netip.MustParseAddr("203.0.113.7"), resp.ExternalAddr)
	})

	t.Run("ipv6 external kept", func(t *testing.T) {
		v6 := netip.MustParseAddr("2001:db8::1")
		b := buildMapResponse(0, 60, nonce, ipProtoUDP, 80, 8080, v6)
		resp, err := decodeMapResponse(b)
		require.NoError(t, err)
		assert.Equal(t, v6, resp.ExternalAddr)
	})

	t.Run("too short", func(t *testing.T) {
		_, err := decodeMapResponse(valid[:40])
		require.Error(t, err)
	})

	t.Run("bad version", func(t *testing.T) {
		b := append([]byte(nil), valid...)
		b[0] = 1
		_, err := decodeMapResponse(b)
		require.Error(t, err)
	})

	t.Run("missing response flag", func(t *testing.T) {
		b := append([]byte(nil), valid...)
		b[1] = opMap // no response flag
		_, err := decodeMapResponse(b)
		require.Error(t, err)
	})

	t.Run("error result code", func(t *testing.T) {
		b := buildMapResponse(8, 0, nonce, ipProtoTCP, 0, 0, netip.IPv6Unspecified())
		_, err := decodeMapResponse(b)
		assert.ErrorIs(t, err, portmapper.ErrNoResources)
	})
}

func TestTo16(t *testing.T) {
	v4 := to16(netip.MustParseAddr("10.0.0.1"))
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 10, 0, 0, 1}, v4[:])

	v6 := to16(netip.MustParseAddr("2001:db8::1"))
	assert.Equal(t, byte(0x20), v6[0])
}

// helpers

func beUint16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }

func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func buildMapResponse(result uint8, lifetime uint32, nonce [nonceLen]byte, proto uint8, iport, eport uint16, ext netip.Addr) []byte {
	b := make([]byte, responseLen)
	b[0] = protoVersion
	b[1] = opMap | responseFlag
	b[3] = result
	b[4] = byte(lifetime >> 24)
	b[5] = byte(lifetime >> 16)
	b[6] = byte(lifetime >> 8)
	b[7] = byte(lifetime)
	copy(b[24:36], nonce[:])
	b[36] = proto
	b[40] = byte(iport >> 8)
	b[41] = byte(iport)
	b[42] = byte(eport >> 8)
	b[43] = byte(eport)
	e := ext.As16()
	copy(b[44:60], e[:])
	return b
}
