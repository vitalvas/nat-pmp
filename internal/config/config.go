// Package config loads and validates the daemon's YAML configuration: the list
// of desired port mappings and protocol-detection settings.
package config

import (
	"fmt"
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
	// ExternalPort is the requested WAN-side port. Zero lets the gateway choose.
	ExternalPort uint16 `yaml:"external_port" json:"external_port"`
	// Description is a human-readable label.
	Description string `yaml:"description" json:"description"`
	// Lease is the requested mapping lifetime.
	Lease time.Duration `yaml:"lease" json:"lease" default:"1h"`
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
// requests. A "both" mapping yields separate TCP and UDP requests.
func (m Mapping) Requests() []mapping.Request {
	protocols := m.protocols()
	requests := make([]mapping.Request, 0, len(protocols))
	for _, proto := range protocols {
		requests = append(requests, mapping.Request{
			Protocol:     proto,
			InternalPort: m.InternalPort,
			ExternalPort: m.ExternalPort,
			Description:  m.Description,
			Lease:        m.Lease,
		})
	}
	return requests
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

// Validate reports whether the configuration is well formed. It rejects empty
// mapping lists, invalid protocols, invalid detection settings, zero internal
// ports, negative leases, duplicate (protocol, internal port) pairs, and
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

	seenExternal := make(map[string]struct{}, len(c.Mappings))
	seenInternal := make(map[string]struct{}, len(c.Mappings))
	for i, m := range c.Mappings {
		requests := m.Requests()
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
