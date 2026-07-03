package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/config"
	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
	"github.com/vitalvas/nat-pmp/internal/portmapper/upnp"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig(mappings ...config.Mapping) config.Config {
	cfg := config.Config{Mappings: mappings}
	cfg.Detect.Timeout = time.Second
	return cfg
}

// fakeClient is a programmable portmapper.Client.
type fakeClient struct {
	mu sync.Mutex

	name       string
	extIP      netip.Addr
	extIPErr   error
	mapErr     error
	unmapErr   error
	lifetime   time.Duration
	mapCalls   int
	unmapCalls int
	mapped     []mapping.Request
	unmapped   []mapping.Request
	// grantedExternal lets a test simulate the gateway assigning a port.
	grantedExternal uint16
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		name:     "fake",
		extIP:    netip.MustParseAddr("203.0.113.1"),
		lifetime: time.Hour,
	}
}

func (f *fakeClient) Name() string { return f.name }

func (f *fakeClient) ExternalIP(context.Context) (netip.Addr, error) {
	return f.extIP, f.extIPErr
}

func (f *fakeClient) Map(_ context.Context, r mapping.Request) (mapping.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mapCalls++
	f.mapped = append(f.mapped, r)
	if f.mapErr != nil {
		return mapping.Lease{}, f.mapErr
	}
	ext := r.ExternalPort
	if f.grantedExternal != 0 {
		ext = f.grantedExternal
	}
	if ext == 0 {
		ext = r.InternalPort
	}
	return mapping.Lease{
		Protocol:     r.Protocol,
		InternalPort: r.InternalPort,
		ExternalPort: ext,
		ExternalIP:   f.extIP,
		Lifetime:     f.lifetime,
		Acquired:     time.Unix(0, 0),
	}, nil
}

func (f *fakeClient) Unmap(_ context.Context, r mapping.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unmapCalls++
	f.unmapped = append(f.unmapped, r)
	return f.unmapErr
}

func (f *fakeClient) counts() (mapCalls, unmapCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mapCalls, f.unmapCalls
}

func newDaemon(t *testing.T, cfg config.Config, client portmapper.Client, opts ...Option) *Daemon {
	t.Helper()
	base := make([]Option, 0, 2+len(opts))
	base = append(base,
		WithGateway(func() (netip.Addr, error) { return netip.MustParseAddr("192.168.1.1"), nil }),
		WithDetect(func(context.Context, netip.Addr) (portmapper.Client, error) { return client, nil }),
	)
	return New(cfg, testLogger(), append(base, opts...)...)
}

func TestRunGatewayError(t *testing.T) {
	d := newDaemon(t, testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80}), newFakeClient(),
		WithGateway(func() (netip.Addr, error) { return netip.Addr{}, assert.AnError }))
	err := d.Run(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunDetectError(t *testing.T) {
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80})
	d := newDaemon(t, cfg, newFakeClient(),
		WithDetect(func(context.Context, netip.Addr) (portmapper.Client, error) {
			return nil, assert.AnError
		}))
	err := d.Run(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunUPnPClientLocalAddressBranches(t *testing.T) {
	runCase := func(t *testing.T, gateway netip.Addr) {
		t.Helper()
		client := upnp.New()
		cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000})
		d := newDaemon(t, cfg, client,
			WithGateway(func() (netip.Addr, error) { return gateway, nil }),
			WithDetect(func(context.Context, netip.Addr) (portmapper.Client, error) { return client, nil }),
		)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, d.Run(ctx))
	}

	t.Run("local address set", func(t *testing.T) {
		runCase(t, netip.MustParseAddr("127.0.0.1"))
	})

	t.Run("local address warning", func(t *testing.T) {
		runCase(t, netip.Addr{})
	})
}

func TestRunCreatesAndReleases(t *testing.T) {
	client := newFakeClient()
	cfg := testConfig(
		config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 22000, Lease: time.Hour},
		config.Mapping{Protocol: "udp", InternalPort: 51820, Lease: time.Hour},
	)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately so the renew loop enters, then exits and releases.
	fired := make(chan struct{})
	d := newDaemon(t, cfg, client, WithClock(time.Now))
	// Replace the timer with one that signals when the loop is waiting, then
	// blocks; cancellation drives shutdown.
	d.newTimer = func(time.Duration) *time.Timer {
		select {
		case <-fired:
		default:
			close(fired)
		}
		return time.NewTimer(time.Hour)
	}

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	<-fired // mappings created, loop is waiting
	mapCalls, _ := client.counts()
	assert.Equal(t, 2, mapCalls)

	cancel()
	require.NoError(t, <-done)
	_, unmapCalls := client.counts()
	assert.Equal(t, 2, unmapCalls)
}

func TestRequestsExpandsBoth(t *testing.T) {
	cfg := testConfig(
		config.Mapping{Protocol: "both", InternalPort: 22000, ExternalPort: 22000},
		config.Mapping{Protocol: "tcp", InternalPort: 80, ExternalPort: 80},
	)
	d := newDaemon(t, cfg, newFakeClient())

	reqs := d.requests()
	require.Len(t, reqs, 3) // both -> tcp+udp, plus one tcp
	assert.Equal(t, mapping.TCP, reqs[0].Protocol)
	assert.Equal(t, mapping.UDP, reqs[1].Protocol)
	assert.Equal(t, mapping.TCP, reqs[2].Protocol)
}

func TestRenewLoopRefreshesDueMappings(t *testing.T) {
	client := newFakeClient()
	client.lifetime = time.Hour
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 22000, Lease: time.Hour})

	// Controllable clock: start at t0; advance past the renewal time on demand.
	var mu sync.Mutex
	current := time.Unix(1_000_000, 0)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(d)
	}

	fireCh := make(chan struct{}, 1)
	timerCh := make(chan time.Time, 1)
	d := newDaemon(t, cfg, client, WithClock(clock))
	d.newTimer = func(time.Duration) *time.Timer {
		// Signal that the loop is armed; return a timer we fire manually.
		select {
		case fireCh <- struct{}{}:
		default:
		}
		tm := time.NewTimer(time.Hour)
		tm.C = timerCh
		return tm
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	<-fireCh // initial mapping created; loop armed
	mapCalls, _ := client.counts()
	assert.Equal(t, 1, mapCalls)

	// Advance the clock past the renewal time and fire the timer.
	advance(time.Hour)
	timerCh <- time.Now()

	<-fireCh // loop re-armed after renewal
	mapCalls, _ = client.counts()
	assert.GreaterOrEqual(t, mapCalls, 2)

	cancel()
	<-done
}

func TestRenewLoopNoScheduleWaitsBackoff(t *testing.T) {
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000})
	d := newDaemon(t, cfg, newFakeClient())
	d.client = newFakeClient()

	armed := make(chan time.Duration, 1)
	d.newTimer = func(wait time.Duration) *time.Timer {
		armed <- wait
		return time.NewTimer(time.Hour)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.renewLoop(ctx) }()

	assert.Equal(t, retryBackoff, <-armed)
	cancel()
	require.NoError(t, <-done)
}

func TestRenewDueSkipsUnscheduledMapping(t *testing.T) {
	client := newFakeClient()
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 22000})
	d := newDaemon(t, cfg, client)
	d.client = client

	d.renewDue(context.Background())

	mapCalls, _ := client.counts()
	assert.Equal(t, 0, mapCalls)
}

func TestReconcileOneFailureSchedulesRetry(t *testing.T) {
	client := newFakeClient()
	client.mapErr = assert.AnError
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80})

	d := newDaemon(t, cfg, client, WithClock(func() time.Time { return time.Unix(1000, 0) }))
	d.client = client
	d.reconcileOne(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 80})

	k := keyOf(mapping.Request{Protocol: mapping.TCP, InternalPort: 80})
	assert.Empty(t, d.leases)
	assert.Equal(t, time.Unix(1000, 0).Add(retryBackoff), d.nextAt[k])
}

func TestReleaseAllPartialFailure(t *testing.T) {
	client := newFakeClient()
	client.unmapErr = assert.AnError
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80})

	d := newDaemon(t, cfg, client)
	d.client = client
	d.leases[keyOf(mapping.Request{Protocol: mapping.TCP, InternalPort: 80})] = mapping.Lease{}

	err := d.releaseAll()
	require.Error(t, err)
}

func TestReleaseAllUsesGrantedExternalPort(t *testing.T) {
	client := newFakeClient()
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 0})

	req := mapping.Request{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 0}
	d := newDaemon(t, cfg, client)
	d.client = client
	d.leases[keyOf(req)] = mapping.Lease{
		Protocol:     mapping.TCP,
		InternalPort: 22000,
		ExternalPort: 40000,
	}

	require.NoError(t, d.releaseAll())
	require.Len(t, client.unmapped, 1)
	assert.Equal(t, uint16(40000), client.unmapped[0].ExternalPort)
}

func TestReleaseAllEmpty(t *testing.T) {
	d := newDaemon(t, testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80}), newFakeClient())
	d.client = newFakeClient()
	require.NoError(t, d.releaseAll())
}

func TestReleaseAllSkipsMissingLeaseForConfiguredMapping(t *testing.T) {
	client := newFakeClient()
	d := newDaemon(t, testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80}), client)
	d.client = client
	d.leases[keyOf(mapping.Request{Protocol: mapping.UDP, InternalPort: 53})] = mapping.Lease{
		Protocol:     mapping.UDP,
		InternalPort: 53,
		ExternalPort: 53,
		Lifetime:     time.Hour,
	}

	require.NoError(t, d.releaseAll())
	_, unmapCalls := client.counts()
	assert.Equal(t, 0, unmapCalls)
}

func TestTimeUntilNext(t *testing.T) {
	now := time.Unix(1000, 0)
	d := newDaemon(t, testConfig(config.Mapping{Protocol: "tcp", InternalPort: 80}), newFakeClient(),
		WithClock(func() time.Time { return now }))

	t.Run("no schedule", func(t *testing.T) {
		_, ok := d.timeUntilNext()
		assert.False(t, ok)
	})

	t.Run("future time", func(t *testing.T) {
		d.nextAt[keyOf(mapping.Request{Protocol: mapping.TCP, InternalPort: 80})] = now.Add(time.Minute)
		wait, ok := d.timeUntilNext()
		require.True(t, ok)
		assert.Equal(t, time.Minute, wait)
	})

	t.Run("past time clamps to zero", func(t *testing.T) {
		d.nextAt[keyOf(mapping.Request{Protocol: mapping.TCP, InternalPort: 80})] = now.Add(-time.Minute)
		wait, ok := d.timeUntilNext()
		require.True(t, ok)
		assert.Equal(t, time.Duration(0), wait)
	})
}

func TestReconcileOneRecordsLease(t *testing.T) {
	client := newFakeClient()
	client.grantedExternal = 40000 // gateway assigns a different port
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 30000})

	d := newDaemon(t, cfg, client, WithClock(func() time.Time { return time.Unix(1000, 0) }))
	d.client = client
	d.reconcileOne(context.Background(), mapping.Request{
		Protocol:     mapping.TCP,
		InternalPort: 22000,
		ExternalPort: 30000,
	})

	lease := d.leases[keyOf(mapping.Request{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 30000})]
	assert.Equal(t, uint16(40000), lease.ExternalPort)
}

func TestReconcileOneRenewsAssignedExternalPort(t *testing.T) {
	client := newFakeClient()
	client.grantedExternal = 40000
	cfg := testConfig(config.Mapping{Protocol: "tcp", InternalPort: 22000, ExternalPort: 0})

	d := newDaemon(t, cfg, client, WithClock(func() time.Time { return time.Unix(1000, 0) }))
	d.client = client
	req := mapping.Request{Protocol: mapping.TCP, InternalPort: 22000, ExternalPort: 0}

	d.reconcileOne(context.Background(), req)
	d.reconcileOne(context.Background(), req)

	require.Len(t, client.mapped, 2)
	assert.Equal(t, uint16(0), client.mapped[0].ExternalPort)
	assert.Equal(t, uint16(40000), client.mapped[1].ExternalPort)
}
