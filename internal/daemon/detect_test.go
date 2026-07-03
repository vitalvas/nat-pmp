package daemon

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

var testGW = netip.MustParseAddr("192.168.1.1")

func probeClient(name string, err error) candidate {
	return candidate{
		name: name,
		newFunc: func(netip.Addr) portmapper.Client {
			c := newFakeClient()
			c.name = name
			c.extIPErr = err
			return c
		},
	}
}

func TestDetect(t *testing.T) {
	t.Run("first success wins", func(t *testing.T) {
		cands := []candidate{
			probeClient("a", assert.AnError),
			probeClient("b", nil),
			probeClient("c", nil),
		}
		client, err := detect(context.Background(), testGW, cands, time.Second)
		require.NoError(t, err)
		assert.Equal(t, "b", client.Name())
	})

	t.Run("all fail", func(t *testing.T) {
		cands := []candidate{
			probeClient("a", assert.AnError),
			probeClient("b", assert.AnError),
		}
		_, err := detect(context.Background(), testGW, cands, time.Second)
		require.Error(t, err)
	})

	t.Run("context cancelled aborts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cands := []candidate{probeClient("a", assert.AnError)}
		_, err := detect(ctx, testGW, cands, time.Second)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestOrderCandidates(t *testing.T) {
	all := []candidate{
		{name: "natpmp"},
		{name: "pcp"},
		{name: "upnp"},
	}

	t.Run("empty order keeps default", func(t *testing.T) {
		got := orderCandidates(all, nil)
		require.Len(t, got, 3)
		assert.Equal(t, "natpmp", got[0].name)
	})

	t.Run("reorders by name", func(t *testing.T) {
		got := orderCandidates(all, []string{"upnp", "natpmp"})
		require.Len(t, got, 3)
		assert.Equal(t, "upnp", got[0].name)
		assert.Equal(t, "natpmp", got[1].name)
		assert.Equal(t, "pcp", got[2].name) // remaining appended
	})

	t.Run("ignores unknown and duplicate names", func(t *testing.T) {
		got := orderCandidates(all, []string{"bogus", "pcp", "pcp"})
		require.Len(t, got, 3)
		assert.Equal(t, "pcp", got[0].name)
	})
}

func TestDefaultCandidates(t *testing.T) {
	cands := defaultCandidates()
	require.Len(t, cands, 3)
	names := []string{cands[0].name, cands[1].name, cands[2].name}
	assert.Equal(t, []string{"natpmp", "pcp", "upnp"}, names)

	// Each factory must build a non-nil client.
	for _, c := range cands {
		assert.NotNil(t, c.newFunc(testGW))
	}
}
