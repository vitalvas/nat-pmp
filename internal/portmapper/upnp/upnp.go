package upnp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// ProtocolName identifies this protocol in logs and detection ordering.
const ProtocolName = "upnp"

// defaultLease is applied when a request omits a lifetime.
const defaultLease = time.Hour

// defaultHTTPTimeout bounds descriptor and SOAP HTTP calls for the built-in
// client. Callers can override it with WithHTTPClient.
const defaultHTTPTimeout = 5 * time.Second

// UPnP protocol names sent in AddPortMapping/DeletePortMapping SOAP arguments.
const (
	wireTCP = "TCP"
	wireUDP = "UDP"
)

// Client speaks UPnP IGD to a gateway discovered via SSDP.
type Client struct {
	doer          httpDoer
	search        searchFunc
	fetch         func(ctx context.Context, url string) ([]byte, error)
	searchTimeout time.Duration
	now           func() time.Time

	mu             sync.Mutex
	target         *controlTarget // resolved lazily and cached
	internalClient string         // LAN address advertised to the gateway
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client used for SOAP and descriptor fetch.
func WithHTTPClient(doer httpDoer) Option {
	return func(c *Client) { c.doer = doer }
}

// WithSearch overrides SSDP discovery. Used in tests.
func WithSearch(s func(ctx context.Context) ([]ssdpResult, error)) Option {
	return func(c *Client) { c.search = searchFunc(s) }
}

// WithFetch overrides descriptor fetching. Used in tests.
func WithFetch(f func(ctx context.Context, url string) ([]byte, error)) Option {
	return func(c *Client) { c.fetch = f }
}

// WithSearchTimeout overrides the SSDP search timeout.
func WithSearchTimeout(d time.Duration) Option {
	return func(c *Client) { c.searchTimeout = d }
}

// WithClock overrides the time source. Used in tests.
func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// New creates a UPnP IGD client.
func New(opts ...Option) *Client {
	c := &Client{
		doer:          &http.Client{Timeout: defaultHTTPTimeout},
		searchTimeout: defaultSearchTimeout,
		now:           time.Now,
	}
	c.search = func(ctx context.Context) ([]ssdpResult, error) {
		return discover(ctx, c.searchTimeout)
	}
	c.fetch = c.fetchHTTP
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies the protocol.
func (c *Client) Name() string { return ProtocolName }

func (c *Client) fetchHTTP(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upnp: descriptor fetch status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// resolveTarget discovers the gateway and resolves its WAN service control URL,
// caching the result for subsequent calls.
func (c *Client) resolveTarget(ctx context.Context) (controlTarget, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.target != nil {
		return *c.target, nil
	}

	results, err := c.search(ctx)
	if err != nil {
		return controlTarget{}, err
	}

	var lastErr error
	for _, res := range results {
		descriptor, err := c.fetch(ctx, res.Location)
		if err != nil {
			lastErr = err
			continue
		}
		target, err := parseControlTarget(descriptor, res.Location)
		if err != nil {
			lastErr = err
			continue
		}
		c.target = &target
		return target, nil
	}
	if lastErr != nil {
		return controlTarget{}, lastErr
	}
	return controlTarget{}, fmt.Errorf("upnp: no usable gateway descriptor")
}

// ExternalIP returns the gateway's WAN address via GetExternalIPAddress.
func (c *Client) ExternalIP(ctx context.Context) (netip.Addr, error) {
	target, err := c.resolveTarget(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	body, err := callSOAP(ctx, c.doer, target, "GetExternalIPAddress", nil)
	if err != nil {
		return netip.Addr{}, err
	}
	value, err := parseStringField(body, "NewExternalIPAddress")
	if err != nil {
		return netip.Addr{}, err
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("upnp: parse external IP %q: %w", value, err)
	}
	return addr, nil
}

// protocolString returns the UPnP protocol name for a transport protocol.
func protocolString(p mapping.Protocol) (string, error) {
	switch p {
	case mapping.TCP:
		return wireTCP, nil
	case mapping.UDP:
		return wireUDP, nil
	default:
		return "", fmt.Errorf("upnp: unsupported protocol %q", p)
	}
}

// Map creates or refreshes a mapping via AddPortMapping and returns the lease.
// UPnP AddPortMapping requires an explicit external port; a zero request port
// defaults to the internal port.
func (c *Client) Map(ctx context.Context, r mapping.Request) (mapping.Lease, error) {
	if err := r.Validate(); err != nil {
		return mapping.Lease{}, err
	}
	proto, _ := protocolString(r.Protocol)
	target, err := c.resolveTarget(ctx)
	if err != nil {
		return mapping.Lease{}, err
	}
	internalClient := c.getInternalClient()
	if internalClient == "" {
		return mapping.Lease{}, fmt.Errorf("upnp: internal client address is required")
	}

	lifetime := r.Lease
	if lifetime <= 0 {
		lifetime = defaultLease
	}
	externalPort := r.ExternalPort
	if externalPort == 0 {
		externalPort = r.InternalPort
	}

	args := []soapArg{
		{Name: "NewRemoteHost", Value: ""},
		{Name: "NewExternalPort", Value: strconv.Itoa(int(externalPort))},
		{Name: "NewProtocol", Value: proto},
		{Name: "NewInternalPort", Value: strconv.Itoa(int(r.InternalPort))},
		{Name: "NewInternalClient", Value: internalClient},
		{Name: "NewEnabled", Value: "1"},
		{Name: "NewPortMappingDescription", Value: r.Description},
		{Name: "NewLeaseDuration", Value: strconv.FormatInt(int64(lifetime/time.Second), 10)},
	}
	if _, err := callSOAP(ctx, c.doer, target, "AddPortMapping", args); err != nil {
		return mapping.Lease{}, err
	}

	extIP, err := c.ExternalIP(ctx)
	if err != nil {
		return mapping.Lease{}, err
	}

	return mapping.Lease{
		Protocol:     r.Protocol,
		InternalPort: r.InternalPort,
		ExternalPort: externalPort,
		ExternalIP:   extIP,
		Lifetime:     lifetime,
		Acquired:     c.now(),
	}, nil
}

// Unmap releases a mapping via DeletePortMapping. Deleting a non-existent
// mapping is treated as success.
func (c *Client) Unmap(ctx context.Context, r mapping.Request) error {
	if err := r.Validate(); err != nil {
		return err
	}
	proto, _ := protocolString(r.Protocol)
	target, err := c.resolveTarget(ctx)
	if err != nil {
		return err
	}
	externalPort := r.ExternalPort
	if externalPort == 0 {
		externalPort = r.InternalPort
	}
	args := []soapArg{
		{Name: "NewRemoteHost", Value: ""},
		{Name: "NewExternalPort", Value: strconv.Itoa(int(externalPort))},
		{Name: "NewProtocol", Value: proto},
	}
	_, err = callSOAP(ctx, c.doer, target, "DeletePortMapping", args)
	return err
}

// SetInternalClient sets the LAN address advertised in AddPortMapping calls.
// UPnP AddPortMapping requires the client's own LAN IP as NewInternalClient;
// the daemon knows this address (it discovers the gateway and its own route to
// it) and sets it before mapping.
func (c *Client) SetInternalClient(addr netip.Addr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !addr.IsValid() || addr.IsUnspecified() {
		c.internalClient = ""
		return
	}
	c.internalClient = addr.String()
}

func (c *Client) getInternalClient() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.internalClient
}
