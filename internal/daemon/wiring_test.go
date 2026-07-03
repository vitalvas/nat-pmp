package daemon

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/config"
)

func TestLocalAddrForInvalidGateway(t *testing.T) {
	_, err := localAddrFor(netip.Addr{})
	require.Error(t, err)
}

func TestDefaultDetectUsesDefaultTimeout(t *testing.T) {
	cfg := config.Config{}
	d := New(cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.defaultDetect(ctx, netip.MustParseAddr("192.0.2.1"))
	require.ErrorIs(t, err, context.Canceled)
}
