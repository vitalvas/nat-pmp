package config

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad(t *testing.T) {
	t.Run("full config with defaults", func(t *testing.T) {
		path := writeConfig(t, `
log_level: debug
mappings:
  - protocol: tcp
    internal_port: 22000
    external_port: 22000
    description: syncthing
    lease: 2h
  - protocol: udp
    internal_port: 51820
`)
		cfg, err := Load(path)
		require.NoError(t, err)

		assert.Equal(t, "debug", cfg.LogLevel)
		assert.Equal(t, 3*time.Second, cfg.Detect.Timeout)
		require.Len(t, cfg.Mappings, 2)

		assert.Equal(t, "tcp", cfg.Mappings[0].Protocol)
		assert.Equal(t, uint16(22000), cfg.Mappings[0].InternalPort)
		assert.Equal(t, 2*time.Hour, cfg.Mappings[0].Lease)

		// Second mapping omits lease -> default 30m applied to slice element.
		assert.Equal(t, 30*time.Minute, cfg.Mappings[1].Lease)
		assert.Equal(t, uint16(0), cfg.Mappings[1].ExternalPort)
	})

	t.Run("default log level", func(t *testing.T) {
		path := writeConfig(t, `
mappings:
  - protocol: tcp
    internal_port: 80
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		assert.Equal(t, "info", cfg.LogLevel)
	})

	t.Run("env override", func(t *testing.T) {
		t.Setenv("NATPMP_LOG_LEVEL", "warn")
		path := writeConfig(t, `
mappings:
  - protocol: tcp
    internal_port: 80
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		assert.Equal(t, "warn", cfg.LogLevel)
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("malformed yaml", func(t *testing.T) {
		path := writeConfig(t, "mappings: [::bad")
		_, err := Load(path)
		require.Error(t, err)
	})

	t.Run("invalid protocol fails validation", func(t *testing.T) {
		path := writeConfig(t, `
mappings:
  - protocol: sctp
    internal_port: 80
`)
		_, err := Load(path)
		require.Error(t, err)
	})

	t.Run("duplicate detect order fails validation", func(t *testing.T) {
		path := writeConfig(t, `
detect:
  order: [pcp, pcp]
mappings:
  - protocol: tcp
    internal_port: 80
`)
		_, err := Load(path)
		require.Error(t, err)
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 22000, ExternalPort: 22000},
				{Protocol: "udp", InternalPort: 22000, ExternalPort: 22000},
			}},
		},
		{
			name:    "empty mappings",
			cfg:     Config{},
			wantErr: true,
		},
		{
			name:    "invalid protocol",
			cfg:     Config{Mappings: []Mapping{{Protocol: "icmp", InternalPort: 80}}},
			wantErr: true,
		},
		{
			name:    "invalid log level",
			cfg:     Config{LogLevel: "verbose", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}},
			wantErr: true,
		},
		{
			name:    "invalid detect order",
			cfg:     Config{Detect: Detect{Order: []string{"pcp", "bogus"}}, Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}},
			wantErr: true,
		},
		{
			name:    "duplicate detect order",
			cfg:     Config{Detect: Detect{Order: []string{"pcp", "pcp"}}, Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}},
			wantErr: true,
		},
		{
			name:    "negative detect timeout",
			cfg:     Config{Detect: Detect{Timeout: -time.Second}, Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}},
			wantErr: true,
		},
		{
			name:    "zero internal port",
			cfg:     Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 0}}},
			wantErr: true,
		},
		{
			name:    "negative lease",
			cfg:     Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, Lease: -time.Second}}},
			wantErr: true,
		},
		{
			name:    "fractional-second lease",
			cfg:     Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, Lease: 1500 * time.Millisecond}}},
			wantErr: true,
		},
		{
			name:    "too large lease",
			cfg:     Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, Lease: mapping.MaxLease + time.Second}}},
			wantErr: true,
		},
		{
			name: "duplicate internal port same protocol",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8081},
			}},
			wantErr: true,
		},
		{
			name: "same internal port different protocol is allowed",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "udp", InternalPort: 80, ExternalPort: 8080},
			}},
		},
		{
			name: "duplicate external port same protocol",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "tcp", InternalPort: 81, ExternalPort: 8080},
			}},
			wantErr: true,
		},
		{
			name: "same external port different protocol is allowed",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "udp", InternalPort: 81, ExternalPort: 8080},
			}},
		},
		{
			name: "multiple zero external ports allowed",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 0},
				{Protocol: "tcp", InternalPort: 81, ExternalPort: 0},
			}},
		},
		{
			name: "both is a valid protocol",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "both", InternalPort: 22000, ExternalPort: 22000},
			}},
		},
		{
			name: "both conflicts with tcp on same internal port",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "both", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "tcp", InternalPort: 80, ExternalPort: 8081},
			}},
			wantErr: true,
		},
		{
			name: "both conflicts with tcp on same external port",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "both", InternalPort: 80, ExternalPort: 8080},
				{Protocol: "tcp", InternalPort: 81, ExternalPort: 8080},
			}},
			wantErr: true,
		},
		{
			name: "both conflicts with udp on same external port",
			cfg: Config{Mappings: []Mapping{
				{Protocol: "udp", InternalPort: 81, ExternalPort: 8080},
				{Protocol: "both", InternalPort: 80, ExternalPort: 8080},
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateInternalAddress(t *testing.T) {
	local := netip.MustParseAddr("192.168.1.50")
	orig := localAddresses
	t.Cleanup(func() { localAddresses = orig })
	localAddresses = func() ([]netip.Addr, error) {
		return []netip.Addr{local, netip.MustParseAddr("10.0.0.5")}, nil
	}

	t.Run("top-level address bound to interface", func(t *testing.T) {
		cfg := Config{InternalAddress: "192.168.1.50", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.NoError(t, cfg.Validate())
	})

	t.Run("mapping address bound to interface", func(t *testing.T) {
		cfg := Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, InternalAddress: "10.0.0.5"}}}
		require.NoError(t, cfg.Validate())
	})

	t.Run("unparseable address rejected", func(t *testing.T) {
		cfg := Config{InternalAddress: "not-an-ip", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.Error(t, cfg.Validate())
	})

	t.Run("address not on any interface rejected", func(t *testing.T) {
		cfg := Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, InternalAddress: "172.16.0.9"}}}
		require.Error(t, cfg.Validate())
	})

	t.Run("interface listing error surfaces", func(t *testing.T) {
		localAddresses = func() ([]netip.Addr, error) { return nil, assert.AnError }
		t.Cleanup(func() {
			localAddresses = func() ([]netip.Addr, error) {
				return []netip.Addr{local}, nil
			}
		})
		cfg := Config{InternalAddress: "192.168.1.50", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.Error(t, cfg.Validate())
	})

	t.Run("address and iface both set at top level rejected", func(t *testing.T) {
		cfg := Config{InternalAddress: "192.168.1.50", InternalIface: "eth0", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.Error(t, cfg.Validate())
	})

	t.Run("address and iface both set on mapping rejected", func(t *testing.T) {
		cfg := Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, InternalAddress: "10.0.0.5", InternalIface: "eth0"}}}
		require.Error(t, cfg.Validate())
	})
}

func TestValidateInternalIface(t *testing.T) {
	origLocal, origIface := localAddresses, ifaceAddresses
	t.Cleanup(func() { localAddresses, ifaceAddresses = origLocal, origIface })

	local := netip.MustParseAddr("192.168.1.50")
	localAddresses = func() ([]netip.Addr, error) {
		return []netip.Addr{local, netip.MustParseAddr("10.0.0.5")}, nil
	}
	ifaceAddresses = func(name string) ([]netip.Addr, error) {
		switch name {
		case "eth0":
			return []netip.Addr{
				netip.MustParseAddr("127.0.0.1"),    // loopback, skipped
				netip.MustParseAddr("169.254.1.1"),  // link-local, skipped
				netip.MustParseAddr("192.168.1.50"), // first usable
			}, nil
		case "empty0":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		default:
			return nil, assert.AnError
		}
	}

	t.Run("top-level iface resolves to first usable IPv4", func(t *testing.T) {
		cfg := Config{InternalIface: "eth0", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.NoError(t, cfg.Validate())
		reqs := cfg.Requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, local, reqs[0].InternalAddress)
	})

	t.Run("mapping iface overrides top-level default", func(t *testing.T) {
		cfg := Config{
			InternalAddress: "10.0.0.5",
			Mappings:        []Mapping{{Protocol: "tcp", InternalPort: 80, InternalIface: "eth0"}},
		}
		require.NoError(t, cfg.Validate())
		reqs := cfg.Requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, local, reqs[0].InternalAddress)
	})

	t.Run("iface without usable IPv4 rejected", func(t *testing.T) {
		cfg := Config{InternalIface: "empty0", Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80}}}
		require.Error(t, cfg.Validate())
	})

	t.Run("unknown iface rejected", func(t *testing.T) {
		cfg := Config{Mappings: []Mapping{{Protocol: "tcp", InternalPort: 80, InternalIface: "wlan9"}}}
		require.Error(t, cfg.Validate())
	})
}

func TestFirstUsableIPv4(t *testing.T) {
	t.Run("skips loopback and link-local", func(t *testing.T) {
		addr, ok := firstUsableIPv4([]netip.Addr{
			netip.MustParseAddr("127.0.0.1"),
			netip.MustParseAddr("169.254.0.7"),
			netip.MustParseAddr("192.168.1.50"),
		})
		require.True(t, ok)
		assert.Equal(t, netip.MustParseAddr("192.168.1.50"), addr)
	})

	t.Run("skips ipv6", func(t *testing.T) {
		addr, ok := firstUsableIPv4([]netip.Addr{
			netip.MustParseAddr("2001:db8::1"),
			netip.MustParseAddr("10.0.0.2"),
		})
		require.True(t, ok)
		assert.Equal(t, netip.MustParseAddr("10.0.0.2"), addr)
	})

	t.Run("none usable", func(t *testing.T) {
		_, ok := firstUsableIPv4([]netip.Addr{netip.MustParseAddr("127.0.0.1")})
		assert.False(t, ok)
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := firstUsableIPv4(nil)
		assert.False(t, ok)
	})
}

func TestDefaultIfaceAddresses(t *testing.T) {
	t.Run("unknown interface errors", func(t *testing.T) {
		_, err := defaultIfaceAddresses("definitely-not-an-iface-xyz")
		require.Error(t, err)
	})

	t.Run("real interface resolves", func(t *testing.T) {
		// Use whatever the host's first interface is named; every host has one.
		ifaces, err := net.Interfaces()
		require.NoError(t, err)
		require.NotEmpty(t, ifaces)
		addrs, err := defaultIfaceAddresses(ifaces[0].Name)
		require.NoError(t, err)
		// The address slice may be empty for an interface with no IPs; the call
		// itself must succeed.
		assert.NotNil(t, addrs)
	})
}

func TestMappingRequests(t *testing.T) {
	t.Run("single protocol yields one request", func(t *testing.T) {
		m := Mapping{
			Protocol:     "tcp",
			InternalPort: 22000,
			ExternalPort: 30000,
			Description:  "test",
			Lease:        time.Hour,
		}
		reqs := m.Requests(netip.Addr{})
		require.Len(t, reqs, 1)
		req := reqs[0]
		assert.Equal(t, mapping.TCP, req.Protocol)
		assert.Equal(t, uint16(22000), req.InternalPort)
		assert.Equal(t, uint16(30000), req.ExternalPort)
		assert.Equal(t, "test", req.Description)
		assert.Equal(t, time.Hour, req.Lease)
		assert.False(t, req.RequireListener)
		assert.False(t, req.InternalAddress.IsValid())
		require.NoError(t, req.Validate())
	})

	t.Run("both expands into tcp and udp", func(t *testing.T) {
		m := Mapping{
			Protocol:        "both",
			InternalPort:    22000,
			ExternalPort:    22000,
			Description:     "test",
			Lease:           time.Hour,
			RequireListener: true,
		}
		reqs := m.Requests(netip.Addr{})
		require.Len(t, reqs, 2)
		assert.Equal(t, mapping.TCP, reqs[0].Protocol)
		assert.Equal(t, mapping.UDP, reqs[1].Protocol)
		for _, req := range reqs {
			assert.Equal(t, uint16(22000), req.InternalPort)
			assert.Equal(t, uint16(22000), req.ExternalPort)
			assert.True(t, req.RequireListener)
			require.NoError(t, req.Validate())
		}
	})

	t.Run("mapping address overrides default", func(t *testing.T) {
		m := Mapping{Protocol: "tcp", InternalPort: 22000, InternalAddress: "192.168.1.50"}
		reqs := m.Requests(netip.MustParseAddr("10.0.0.1"))
		require.Len(t, reqs, 1)
		assert.Equal(t, netip.MustParseAddr("192.168.1.50"), reqs[0].InternalAddress)
	})

	t.Run("empty mapping address inherits default", func(t *testing.T) {
		m := Mapping{Protocol: "tcp", InternalPort: 22000}
		reqs := m.Requests(netip.MustParseAddr("10.0.0.1"))
		require.Len(t, reqs, 1)
		assert.Equal(t, netip.MustParseAddr("10.0.0.1"), reqs[0].InternalAddress)
	})

	t.Run("unparseable mapping address yields zero", func(t *testing.T) {
		m := Mapping{Protocol: "tcp", InternalPort: 22000, InternalAddress: "bogus"}
		reqs := m.Requests(netip.MustParseAddr("10.0.0.1"))
		require.Len(t, reqs, 1)
		assert.False(t, reqs[0].InternalAddress.IsValid())
	})
}

func TestConfigRequests(t *testing.T) {
	cfg := Config{
		InternalAddress: "10.0.0.1",
		Mappings: []Mapping{
			{Protocol: "tcp", InternalPort: 80},
			{Protocol: "both", InternalPort: 81, InternalAddress: "10.0.0.2"},
		},
	}
	reqs := cfg.Requests()
	require.Len(t, reqs, 3) // tcp + (tcp,udp)
	assert.Equal(t, netip.MustParseAddr("10.0.0.1"), reqs[0].InternalAddress)
	assert.Equal(t, netip.MustParseAddr("10.0.0.2"), reqs[1].InternalAddress)
	assert.Equal(t, netip.MustParseAddr("10.0.0.2"), reqs[2].InternalAddress)
}

func TestConfigRequestsUnparseableDefault(t *testing.T) {
	// Requests does not validate; an unparseable top-level address resolves to
	// the zero address rather than panicking.
	cfg := Config{
		InternalAddress: "bogus",
		Mappings:        []Mapping{{Protocol: "tcp", InternalPort: 80}},
	}
	reqs := cfg.Requests()
	require.Len(t, reqs, 1)
	assert.False(t, reqs[0].InternalAddress.IsValid())
}

func TestDefaultLocalAddresses(t *testing.T) {
	addrs, err := defaultLocalAddresses()
	require.NoError(t, err)
	// Every host has at least a loopback address.
	assert.NotEmpty(t, addrs)
}

// fakeAddr is a net.Addr with a controllable String value.
type fakeAddr struct{ s string }

func (a fakeAddr) Network() string { return "ip+net" }
func (a fakeAddr) String() string  { return a.s }

func TestParseInterfaceAddrs(t *testing.T) {
	in := []net.Addr{
		fakeAddr{s: "192.168.1.50/24"},
		fakeAddr{s: "not-a-cidr"}, // skipped
		fakeAddr{s: "10.0.0.5/8"},
	}
	got := parseInterfaceAddrs(in)
	require.Len(t, got, 2)
	assert.Equal(t, netip.MustParseAddr("192.168.1.50"), got[0])
	assert.Equal(t, netip.MustParseAddr("10.0.0.5"), got[1])
}
