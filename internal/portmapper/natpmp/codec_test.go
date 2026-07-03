package natpmp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

func TestResultError(t *testing.T) {
	tests := []struct {
		name string
		code uint16
		want error
	}{
		{name: "success", code: 0, want: nil},
		{name: "unsupported version", code: 1, want: portmapper.ErrUnsupported},
		{name: "not authorized", code: 2, want: portmapper.ErrNotAuthorized},
		{name: "out of resources", code: 4, want: portmapper.ErrNoResources},
		{name: "unsupported opcode", code: 5, want: portmapper.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, resultError(tt.code), tt.want)
		})
	}

	t.Run("network failure is generic error", func(t *testing.T) {
		err := resultError(3)
		require.Error(t, err)
		assert.NotErrorIs(t, err, portmapper.ErrUnsupported)
	})
}

func TestMapOpcode(t *testing.T) {
	udp, err := mapOpcode(mapping.UDP)
	require.NoError(t, err)
	assert.Equal(t, byte(opMapUDP), udp)

	tcp, err := mapOpcode(mapping.TCP)
	require.NoError(t, err)
	assert.Equal(t, byte(opMapTCP), tcp)

	_, err = mapOpcode(mapping.Protocol("bad"))
	require.Error(t, err)
}

func TestEncodeExternalAddressRequest(t *testing.T) {
	assert.Equal(t, []byte{0, 0}, encodeExternalAddressRequest())
}

func TestDecodeExternalAddressResponse(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// version 0, opcode 128, result 0, epoch 0x01020304, IP 203.0.113.5
		b := []byte{0, 128, 0, 0, 0x01, 0x02, 0x03, 0x04, 203, 0, 113, 5}
		resp, err := decodeExternalAddressResponse(b)
		require.NoError(t, err)
		assert.Equal(t, uint32(0x01020304), resp.Epoch)
		assert.Equal(t, [4]byte{203, 0, 113, 5}, resp.ExternalIP)
	})

	t.Run("too short", func(t *testing.T) {
		_, err := decodeExternalAddressResponse([]byte{0, 128, 0})
		require.Error(t, err)
	})

	t.Run("bad version", func(t *testing.T) {
		b := []byte{9, 128, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
		_, err := decodeExternalAddressResponse(b)
		require.Error(t, err)
	})

	t.Run("bad opcode", func(t *testing.T) {
		b := []byte{0, 200, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
		_, err := decodeExternalAddressResponse(b)
		require.Error(t, err)
	})

	t.Run("error result code", func(t *testing.T) {
		b := []byte{0, 128, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
		_, err := decodeExternalAddressResponse(b)
		assert.ErrorIs(t, err, portmapper.ErrNotAuthorized)
	})
}

func TestEncodeMapRequest(t *testing.T) {
	t.Run("tcp round trip", func(t *testing.T) {
		b, err := encodeMapRequest(mapping.TCP, 22000, 30000, 3600)
		require.NoError(t, err)
		want := []byte{
			0, opMapTCP, // version, opcode
			0, 0, // reserved
			0x55, 0xf0, // internal 22000
			0x75, 0x30, // external 30000
			0x00, 0x00, 0x0e, 0x10, // lifetime 3600
		}
		assert.Equal(t, want, b)
	})

	t.Run("bad protocol", func(t *testing.T) {
		_, err := encodeMapRequest(mapping.Protocol("x"), 1, 2, 3)
		require.Error(t, err)
	})
}

func TestDecodeMapResponse(t *testing.T) {
	// version 0, opcode 130 (tcp resp), result 0, epoch, internal 22000,
	// external 30000, lifetime 3600
	valid := []byte{
		0, opMapTCP + opResponseFlag,
		0, 0,
		0, 0, 0, 1,
		0x55, 0xf0,
		0x75, 0x30,
		0x00, 0x00, 0x0e, 0x10,
	}

	t.Run("success", func(t *testing.T) {
		resp, err := decodeMapResponse(valid, mapping.TCP)
		require.NoError(t, err)
		assert.Equal(t, uint16(22000), resp.InternalPort)
		assert.Equal(t, uint16(30000), resp.ExternalPort)
		assert.Equal(t, uint32(3600), resp.LifetimeSec)
		assert.Equal(t, uint32(1), resp.Epoch)
	})

	t.Run("too short", func(t *testing.T) {
		_, err := decodeMapResponse(valid[:10], mapping.TCP)
		require.Error(t, err)
	})

	t.Run("bad version", func(t *testing.T) {
		b := append([]byte(nil), valid...)
		b[0] = 5
		_, err := decodeMapResponse(b, mapping.TCP)
		require.Error(t, err)
	})

	t.Run("opcode mismatches protocol", func(t *testing.T) {
		// Response is a TCP response but we ask to decode as UDP.
		_, err := decodeMapResponse(valid, mapping.UDP)
		require.Error(t, err)
	})

	t.Run("bad protocol", func(t *testing.T) {
		_, err := decodeMapResponse(valid, mapping.Protocol("x"))
		require.Error(t, err)
	})

	t.Run("error result", func(t *testing.T) {
		b := append([]byte(nil), valid...)
		b[2], b[3] = 0, 4 // out of resources
		_, err := decodeMapResponse(b, mapping.TCP)
		assert.ErrorIs(t, err, portmapper.ErrNoResources)
	})
}
