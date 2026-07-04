package config

import (
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

		// Second mapping omits lease -> default 1h applied to slice element.
		assert.Equal(t, time.Hour, cfg.Mappings[1].Lease)
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

func TestMappingRequests(t *testing.T) {
	t.Run("single protocol yields one request", func(t *testing.T) {
		m := Mapping{
			Protocol:     "tcp",
			InternalPort: 22000,
			ExternalPort: 30000,
			Description:  "test",
			Lease:        time.Hour,
		}
		reqs := m.Requests()
		require.Len(t, reqs, 1)
		req := reqs[0]
		assert.Equal(t, mapping.TCP, req.Protocol)
		assert.Equal(t, uint16(22000), req.InternalPort)
		assert.Equal(t, uint16(30000), req.ExternalPort)
		assert.Equal(t, "test", req.Description)
		assert.Equal(t, time.Hour, req.Lease)
		assert.False(t, req.RequireListener)
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
		reqs := m.Requests()
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
}
