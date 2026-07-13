// Package config loads and validates the daemon's YAML configuration: the list
// of desired port mappings and protocol-detection settings.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/vitalvas/gokit/xconfig"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// EnvPrefix is the prefix used for environment-variable overrides.
const EnvPrefix = "NATPMP"

// DefaultDetectTimeout is the per-protocol detection timeout used when no
// timeout is configured.
const DefaultDetectTimeout = 3 * time.Second

// ProtocolBoth is a config-only protocol value that expands into separate TCP
// and UDP mappings. The wire protocols map one transport per request, so this
// is purely a convenience.
const ProtocolBoth = "both"

// Port-mapping protocol names accepted in detect.order.
const (
	ProtocolNATPMP = "natpmp"
	ProtocolPCP    = "pcp"
	ProtocolUPnP   = "upnp"
)

// Config is the top-level daemon configuration.
type Config struct {
	// LogLevel is the slog level: debug, info, warn, or error.
	LogLevel string `yaml:"log_level" json:"log_level" default:"info"`
	// InternalAddress is the default LAN address that mappings forward to. It
	// applies to every mapping that does not set its own internal_address. An
	// empty value lets the daemon derive the address from the route to the
	// gateway.
	InternalAddress string `yaml:"internal_address" json:"internal_address"`
	// InternalIface is the default network interface whose first usable IPv4
	// address mappings forward to. It is mutually exclusive with
	// internal_address at the same level. An empty value falls back to
	// internal_address or the auto-derived address.
	InternalIface string `yaml:"internal_iface" json:"internal_iface"`
	// Mappings is the list of desired port forwardings.
	Mappings []Mapping `yaml:"mappings" json:"mappings"`
	// Detect configures protocol auto-detection.
	Detect Detect `yaml:"detect" json:"detect"`
}

// Detect configures how the daemon auto-detects the gateway's protocol.
type Detect struct {
	// Order optionally overrides the protocol probe order. Valid entries are
	// "natpmp", "pcp", and "upnp". An empty list uses the default order.
	Order []string `yaml:"order" json:"order"`
	// Timeout bounds each protocol probe during detection.
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"3s"`
}

// Mapping is a single desired port forwarding declared in the config.
type Mapping struct {
	// Protocol is the transport protocol: tcp, udp, or both.
	Protocol string `yaml:"protocol" json:"protocol"`
	// InternalPort is the port on this host that traffic is forwarded to.
	InternalPort uint16 `yaml:"internal_port" json:"internal_port"`
	// InternalAddress is the LAN address this mapping forwards to. It overrides
	// the top-level internal_address. An empty value inherits the top-level
	// default, or the auto-derived address when neither is set.
	InternalAddress string `yaml:"internal_address" json:"internal_address"`
	// InternalIface is the network interface whose first usable IPv4 address
	// this mapping forwards to. It overrides the top-level default and is
	// mutually exclusive with internal_address on the same mapping.
	InternalIface string `yaml:"internal_iface" json:"internal_iface"`
	// ExternalPort is the requested WAN-side port. Zero lets the gateway choose.
	ExternalPort uint16 `yaml:"external_port" json:"external_port"`
	// Description is a human-readable label.
	Description string `yaml:"description" json:"description"`
	// Lease is the requested mapping lifetime.
	Lease time.Duration `yaml:"lease" json:"lease" default:"30m"`
	// RequireListener gates the mapping on a local listener bound to
	// InternalPort: the forwarding is only created while a listener is present
	// and is released when it disappears.
	RequireListener bool `yaml:"require_listener" json:"require_listener"`
}

// protocols returns the transport protocols a mapping covers: one for tcp/udp,
// both for the "both" convenience value.
func (m Mapping) protocols() []mapping.Protocol {
	if m.Protocol == ProtocolBoth {
		return []mapping.Protocol{mapping.TCP, mapping.UDP}
	}
	return []mapping.Protocol{mapping.Protocol(m.Protocol)}
}

// Requests converts the config mapping into one or more protocol-agnostic domain
// requests, resolving the internal address against the given default. A "both"
// mapping yields separate TCP and UDP requests.
func (m Mapping) Requests(defaultAddr netip.Addr) []mapping.Request {
	addr, _ := m.internalAddress(defaultAddr)
	protocols := m.protocols()
	requests := make([]mapping.Request, 0, len(protocols))
	for _, proto := range protocols {
		requests = append(requests, mapping.Request{
			Protocol:        proto,
			InternalPort:    m.InternalPort,
			InternalAddress: addr,
			ExternalPort:    m.ExternalPort,
			Description:     m.Description,
			Lease:           m.Lease,
			RequireListener: m.RequireListener,
		})
	}
	return requests
}

// internalAddress resolves the mapping's effective internal address: its own
// internal_address or internal_iface when set, otherwise the supplied default.
func (m Mapping) internalAddress(defaultAddr netip.Addr) (netip.Addr, error) {
	addr, set, err := resolveInternalAddress(m.InternalAddress, m.InternalIface)
	if err != nil {
		return netip.Addr{}, err
	}
	if set {
		return addr, nil
	}
	return defaultAddr, nil
}

// Requests expands every configured mapping into protocol-agnostic domain
// requests, resolving each mapping's internal address against the top-level
// default.
func (c Config) Requests() []mapping.Request {
	defaultAddr, _ := c.defaultInternalAddress()
	reqs := make([]mapping.Request, 0, len(c.Mappings))
	for _, m := range c.Mappings {
		reqs = append(reqs, m.Requests(defaultAddr)...)
	}
	return reqs
}

// defaultInternalAddress resolves the top-level effective internal address from
// internal_address or internal_iface. It is the zero address when neither is
// set.
func (c Config) defaultInternalAddress() (netip.Addr, error) {
	addr, _, err := resolveInternalAddress(c.InternalAddress, c.InternalIface)
	return addr, err
}

// resolveInternalAddress resolves an (address, iface) pair to a single address.
// It reports whether either source was set. An address string is parsed
// directly; an iface name resolves to its first usable IPv4 address. Callers
// must reject the case where both are set; this function prefers the address if
// that ever occurs.
func resolveInternalAddress(address, iface string) (addr netip.Addr, set bool, err error) {
	if address != "" {
		parsed, perr := netip.ParseAddr(address)
		if perr != nil {
			return netip.Addr{}, true, fmt.Errorf("config: invalid internal_address %q: %w", address, perr)
		}
		return parsed, true, nil
	}
	if iface != "" {
		resolved, rerr := resolveIface(iface)
		if rerr != nil {
			return netip.Addr{}, true, rerr
		}
		return resolved, true, nil
	}
	return netip.Addr{}, false, nil
}

// Load reads the configuration from the given YAML file, applying defaults and
// environment-variable overrides, then validates the result.
func Load(filename string) (Config, error) {
	if _, err := os.Stat(filename); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := xconfig.Load(&cfg, xconfig.WithFiles(filename), xconfig.WithEnv(EnvPrefix)); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// localAddresses returns this host's interface addresses. It is a variable so
// tests can supply a fixed set instead of the machine's real interfaces.
var localAddresses = defaultLocalAddresses

// ifaceAddresses returns the addresses bound to the named interface. It is a
// variable so tests can supply a fixed set instead of the real interface.
var ifaceAddresses = defaultIfaceAddresses

// defaultLocalAddresses returns the addresses bound to this host's interfaces.
func defaultLocalAddresses() ([]netip.Addr, error) {
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	return parseInterfaceAddrs(ifaceAddrs), nil
}

// defaultIfaceAddresses returns the addresses bound to the named interface.
func defaultIfaceAddresses(name string) ([]netip.Addr, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	ifaceAddrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	return parseInterfaceAddrs(ifaceAddrs), nil
}

// parseInterfaceAddrs extracts the IP from each interface address, skipping any
// entry that does not parse as a CIDR prefix.
func parseInterfaceAddrs(ifaceAddrs []net.Addr) []netip.Addr {
	addrs := make([]netip.Addr, 0, len(ifaceAddrs))
	for _, ia := range ifaceAddrs {
		prefix, err := netip.ParsePrefix(ia.String())
		if err != nil {
			continue
		}
		addrs = append(addrs, prefix.Addr())
	}
	return addrs
}

// firstUsableIPv4 returns the first IPv4 address in addrs that is not a
// loopback or link-local address, which are unusable as a forwarding target.
func firstUsableIPv4(addrs []netip.Addr) (netip.Addr, bool) {
	for _, a := range addrs {
		a = a.Unmap()
		if !a.Is4() {
			continue
		}
		if a.IsLoopback() || a.IsLinkLocalUnicast() {
			continue
		}
		return a, true
	}
	return netip.Addr{}, false
}

// resolveIface resolves the named interface to its first usable IPv4 address.
func resolveIface(name string) (netip.Addr, error) {
	addrs, err := ifaceAddresses(name)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("config: interface %q: %w", name, err)
	}
	addr, ok := firstUsableIPv4(addrs)
	if !ok {
		return netip.Addr{}, fmt.Errorf("config: interface %q has no usable IPv4 address", name)
	}
	return addr, nil
}

// Validate reports whether the configuration is well formed. It rejects empty
// mapping lists, invalid protocols, invalid detection settings, zero internal
// ports, negative leases, internal addresses that are unparseable or not bound
// to a local interface, duplicate (protocol, internal port) pairs, and
// duplicate non-zero (protocol, external port) pairs. A "both" mapping is
// validated as its expanded TCP and UDP forms.
func (c Config) Validate() error {
	if len(c.Mappings) == 0 {
		return fmt.Errorf("config: at least one mapping is required")
	}

	if err := validateLogLevel(c.LogLevel); err != nil {
		return err
	}
	if err := validateDetect(c.Detect); err != nil {
		return err
	}
	defaultAddr, err := c.validateInternalAddresses()
	if err != nil {
		return err
	}

	seenExternal := make(map[string]struct{}, len(c.Mappings))
	seenInternal := make(map[string]struct{}, len(c.Mappings))
	for i, m := range c.Mappings {
		requests := m.Requests(defaultAddr)
		for _, req := range requests {
			if err := req.Validate(); err != nil {
				return fmt.Errorf("config: mapping %d: %w", i, err)
			}
		}

		for _, req := range requests {
			proto := req.Protocol
			internalKey := fmt.Sprintf("%s/%d", proto, m.InternalPort)
			if _, dup := seenInternal[internalKey]; dup {
				return fmt.Errorf("config: mapping %d: duplicate internal port %s/%d", i, proto, m.InternalPort)
			}
			seenInternal[internalKey] = struct{}{}

			if m.ExternalPort == 0 {
				continue
			}
			externalKey := fmt.Sprintf("%s/%d", proto, m.ExternalPort)
			if _, dup := seenExternal[externalKey]; dup {
				return fmt.Errorf("config: mapping %d: duplicate external port %s/%d", i, proto, m.ExternalPort)
			}
			seenExternal[externalKey] = struct{}{}
		}
	}
	return nil
}

// validateInternalAddresses resolves and checks every configured internal
// address source: the top-level default and each per-mapping override. Setting
// both internal_address and internal_iface at the same level is rejected. Each
// resolved address (from internal_address; iface-resolved addresses come from
// an interface already) must be bound to a local interface. It returns the
// resolved top-level default for reuse.
func (c Config) validateInternalAddresses() (netip.Addr, error) {
	if c.InternalAddress != "" && c.InternalIface != "" {
		return netip.Addr{}, fmt.Errorf("config: internal_address and internal_iface are mutually exclusive")
	}
	defaultAddr, err := c.defaultInternalAddress()
	if err != nil {
		return netip.Addr{}, err
	}

	resolved := make([]netip.Addr, 0, len(c.Mappings)+1)
	if c.InternalAddress != "" {
		resolved = append(resolved, defaultAddr)
	}
	for i, m := range c.Mappings {
		if m.InternalAddress != "" && m.InternalIface != "" {
			return netip.Addr{}, fmt.Errorf("config: mapping %d: internal_address and internal_iface are mutually exclusive", i)
		}
		addr, err := m.internalAddress(defaultAddr)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("config: mapping %d: %w", i, err)
		}
		// Only an explicit per-mapping internal_address needs the local-binding
		// check; iface-resolved addresses are local by construction, and an
		// inherited default is checked at its own level.
		if m.InternalAddress != "" && addr.IsValid() {
			resolved = append(resolved, addr)
		}
	}
	if len(resolved) == 0 {
		return defaultAddr, nil
	}

	local, err := localAddresses()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("config: list local interface addresses: %w", err)
	}
	localSet := make(map[netip.Addr]struct{}, len(local))
	for _, a := range local {
		localSet[a] = struct{}{}
	}
	for _, addr := range resolved {
		if _, ok := localSet[addr]; !ok {
			return netip.Addr{}, fmt.Errorf("config: internal_address %s is not bound to any local interface", addr)
		}
	}
	return defaultAddr, nil
}

func validateLogLevel(level string) error {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "debug", "info", "warn", "warning", "error":
		return nil
	default:
		return fmt.Errorf("config: invalid log_level %q", level)
	}
}

func validateDetect(d Detect) error {
	if d.Timeout < 0 {
		return fmt.Errorf("config: detect.timeout must not be negative")
	}
	seen := make(map[string]struct{}, len(d.Order))
	for i, name := range d.Order {
		switch name {
		case ProtocolNATPMP, ProtocolPCP, ProtocolUPnP:
		default:
			return fmt.Errorf("config: detect.order %d: invalid protocol %q", i, name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("config: detect.order %d: duplicate protocol %q", i, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
