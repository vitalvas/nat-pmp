package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

func TestEnsure(t *testing.T) {
	t.Run("success returns lease", func(t *testing.T) {
		client := newFakeClient()
		lease, err := ensure(context.Background(), client, testLogger(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 22000,
			Lease:        time.Hour,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(22000), lease.ExternalPort)
	})

	t.Run("map error propagates", func(t *testing.T) {
		client := newFakeClient()
		client.mapErr = assert.AnError
		_, err := ensure(context.Background(), client, testLogger(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
		})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("logs assigned port difference", func(t *testing.T) {
		client := newFakeClient()
		client.grantedExternal = 40000
		lease, err := ensure(context.Background(), client, testLogger(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(40000), lease.ExternalPort)
	})

	t.Run("rejects invalid lease", func(t *testing.T) {
		client := newFakeClient()
		client.lifetime = 0
		_, err := ensure(context.Background(), client, testLogger(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 22000,
		})
		require.Error(t, err)
	})
}

func TestValidateLease(t *testing.T) {
	req := mapping.Request{Protocol: mapping.TCP, InternalPort: 22000}
	valid := mapping.Lease{
		Protocol:     mapping.TCP,
		InternalPort: 22000,
		ExternalPort: 30000,
		Lifetime:     time.Hour,
	}

	tests := []struct {
		name  string
		lease mapping.Lease
	}{
		{name: "protocol mismatch", lease: mapping.Lease{Protocol: mapping.UDP, InternalPort: 22000, ExternalPort: 30000, Lifetime: time.Hour}},
		{name: "internal port mismatch", lease: mapping.Lease{Protocol: mapping.TCP, InternalPort: 12345, ExternalPort: 30000, Lifetime: time.Hour}},
		{name: "zero external port", lease: mapping.Lease{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 0, Lifetime: time.Hour}},
		{name: "zero lifetime", lease: mapping.Lease{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 30000, Lifetime: 0}},
	}
	require.NoError(t, validateLease(req, valid))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, validateLease(req, tt.lease))
		})
	}
}
