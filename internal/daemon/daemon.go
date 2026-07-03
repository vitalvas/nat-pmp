// Package daemon runs the port-forwarding lifecycle: it discovers the gateway,
// auto-detects the port-mapping protocol, creates the configured mappings,
// renews them before their leases expire, and releases them on shutdown.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/vitalvas/nat-pmp/internal/config"
	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
	"github.com/vitalvas/nat-pmp/internal/portmapper/upnp"
)

// releaseTimeout bounds the best-effort release of each mapping on shutdown.
const releaseTimeout = 5 * time.Second

// retryBackoff is how long to wait before retrying a mapping that failed to
// create or renew.
const retryBackoff = 30 * time.Second

// gatewayFunc discovers the LAN gateway address.
type gatewayFunc func() (netip.Addr, error)

// detectFunc selects a working port-mapping client for the gateway.
type detectFunc func(ctx context.Context, gateway netip.Addr) (portmapper.Client, error)

// Daemon maintains the configured port mappings.
type Daemon struct {
	cfg      config.Config
	log      *slog.Logger
	gateway  gatewayFunc
	detect   detectFunc
	now      func() time.Time
	newTimer func(d time.Duration) *time.Timer

	// state, only touched by Run's single goroutine
	client portmapper.Client
	leases map[key]mapping.Lease
	nextAt map[key]time.Time
}

// key uniquely identifies a configured mapping.
type key struct {
	protocol     mapping.Protocol
	internalPort uint16
	externalPort uint16
}

func keyOf(r mapping.Request) key {
	return key{protocol: r.Protocol, internalPort: r.InternalPort, externalPort: r.ExternalPort}
}

// Option customizes a Daemon.
type Option func(*Daemon)

// WithGateway overrides gateway discovery. Used in tests.
func WithGateway(f gatewayFunc) Option {
	return func(d *Daemon) { d.gateway = f }
}

// WithDetect overrides protocol detection. Used in tests.
func WithDetect(f detectFunc) Option {
	return func(d *Daemon) { d.detect = f }
}

// WithClock overrides the time source. Used in tests.
func WithClock(now func() time.Time) Option {
	return func(d *Daemon) { d.now = now }
}

// New builds a daemon from configuration.
func New(cfg config.Config, log *slog.Logger, opts ...Option) *Daemon {
	d := &Daemon{
		cfg:      cfg,
		log:      log,
		gateway:  defaultGateway,
		now:      time.Now,
		newTimer: time.NewTimer,
		leases:   make(map[key]mapping.Lease),
		nextAt:   make(map[key]time.Time),
	}
	d.detect = d.defaultDetect
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Run discovers the gateway, detects the protocol, creates the mappings, and
// keeps them renewed until ctx is cancelled, at which point it releases them.
func (d *Daemon) Run(ctx context.Context) error {
	gateway, err := d.gateway()
	if err != nil {
		return fmt.Errorf("daemon: discover gateway: %w", err)
	}
	d.log.Info("gateway discovered", "gateway", gateway)

	client, err := d.detect(ctx, gateway)
	if err != nil {
		return err
	}
	d.client = client
	d.log.Info("protocol detected", "protocol", client.Name())

	// UPnP needs to advertise this host's LAN address; derive it from the route
	// to the gateway.
	if u, ok := client.(*upnp.Client); ok {
		if local, err := localAddrFor(gateway); err == nil {
			u.SetInternalClient(local)
		} else {
			d.log.Warn("could not determine local address for UPnP", "error", err)
		}
	}

	d.reconcileAll(ctx)

	return d.renewLoop(ctx)
}

// requests expands the configured mappings into individual protocol-agnostic
// requests. A "both" mapping expands into separate TCP and UDP requests.
func (d *Daemon) requests() []mapping.Request {
	var reqs []mapping.Request
	for _, m := range d.cfg.Mappings {
		reqs = append(reqs, m.Requests()...)
	}
	return reqs
}

// reconcileAll creates or refreshes every configured mapping, recording leases
// and schedules. Failures are logged and rescheduled for a near-term retry.
func (d *Daemon) reconcileAll(ctx context.Context) {
	for _, req := range d.requests() {
		d.reconcileOne(ctx, req)
	}
}

func (d *Daemon) reconcileOne(ctx context.Context, req mapping.Request) {
	k := keyOf(req)
	mapReq := d.mapRequest(k, req)
	lease, err := ensure(ctx, d.client, d.log, mapReq)
	if err != nil {
		d.nextAt[k] = d.now().Add(retryBackoff)
		return
	}
	d.leases[k] = lease
	d.nextAt[k] = lease.RenewBefore()
}

// mapRequest preserves an automatically assigned external port across
// renewals. The configured request remains the schedule key, so a mapping whose
// initial external_port was zero still has stable daemon state.
func (d *Daemon) mapRequest(k key, req mapping.Request) mapping.Request {
	if req.ExternalPort != 0 {
		return req
	}
	lease, ok := d.leases[k]
	if !ok || lease.ExternalPort == 0 {
		return req
	}
	req.ExternalPort = lease.ExternalPort
	return req
}

// renewLoop waits for the soonest scheduled renewal and refreshes due mappings,
// until the context is cancelled. On exit it releases all active mappings.
func (d *Daemon) renewLoop(ctx context.Context) error {
	for {
		wait, hasNext := d.timeUntilNext()
		if !hasNext {
			// No mappings scheduled (all persistently failing with none active);
			// wait a backoff before retrying rather than spinning.
			wait = retryBackoff
		}

		timer := d.newTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return d.releaseAll()
		case <-timer.C:
			d.renewDue(ctx)
		}
	}
}

// timeUntilNext returns how long until the soonest scheduled action.
func (d *Daemon) timeUntilNext() (time.Duration, bool) {
	var soonest time.Time
	found := false
	for _, at := range d.nextAt {
		if !found || at.Before(soonest) {
			soonest = at
			found = true
		}
	}
	if !found {
		return 0, false
	}
	wait := max(soonest.Sub(d.now()), 0)
	return wait, true
}

// renewDue refreshes every mapping whose scheduled time has arrived.
func (d *Daemon) renewDue(ctx context.Context) {
	now := d.now()
	for _, req := range d.requests() {
		k := keyOf(req)
		at, ok := d.nextAt[k]
		if !ok || at.After(now) {
			continue
		}
		d.reconcileOne(ctx, req)
	}
}

// releaseAll releases every active mapping on a best-effort basis, using a fresh
// context because the parent has been cancelled.
func (d *Daemon) releaseAll() error {
	if len(d.leases) == 0 {
		return nil
	}
	d.log.Info("releasing mappings", "count", len(d.leases))

	var errs []error
	for _, req := range d.requests() {
		k := keyOf(req)
		lease, ok := d.leases[k]
		if !ok {
			continue
		}
		releaseReq := releaseRequest(req, lease)
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		err := d.client.Unmap(ctx, releaseReq)
		cancel()
		if err != nil {
			d.log.Warn("release failed", "protocol", req.Protocol, "internal_port", req.InternalPort, "error", err)
			errs = append(errs, err)
			continue
		}
		delete(d.leases, k)
	}
	return errors.Join(errs...)
}

func releaseRequest(req mapping.Request, lease mapping.Lease) mapping.Request {
	if lease.ExternalPort != 0 {
		req.ExternalPort = lease.ExternalPort
	}
	return req
}
