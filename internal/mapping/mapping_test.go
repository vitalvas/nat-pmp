package mapping

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProtocol(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Protocol
		wantErr bool
	}{
		{name: "tcp", input: "tcp", want: TCP},
		{name: "udp", input: "udp", want: UDP},
		{name: "uppercase not accepted", input: "TCP", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "unknown", input: "sctp", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProtocol(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProtocolValid(t *testing.T) {
	tests := []struct {
		name  string
		proto Protocol
		want  bool
	}{
		{name: "tcp", proto: TCP, want: true},
		{name: "udp", proto: UDP, want: true},
		{name: "empty", proto: Protocol(""), want: false},
		{name: "other", proto: Protocol("icmp"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.proto.Valid())
		})
	}
}

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{
			name: "valid tcp",
			req:  Request{Protocol: TCP, InternalPort: 22000, ExternalPort: 22000},
		},
		{
			name: "valid udp zero external",
			req:  Request{Protocol: UDP, InternalPort: 51820, ExternalPort: 0},
		},
		{
			name: "valid whole-second lease",
			req:  Request{Protocol: TCP, InternalPort: 22000, Lease: time.Second},
		},
		{
			name:    "invalid protocol",
			req:     Request{Protocol: Protocol("bad"), InternalPort: 22000},
			wantErr: true,
		},
		{
			name:    "zero internal port",
			req:     Request{Protocol: TCP, InternalPort: 0},
			wantErr: true,
		},
		{
			name:    "negative lease",
			req:     Request{Protocol: TCP, InternalPort: 22000, Lease: -time.Second},
			wantErr: true,
		},
		{
			name:    "sub-second lease",
			req:     Request{Protocol: TCP, InternalPort: 22000, Lease: 500 * time.Millisecond},
			wantErr: true,
		},
		{
			name:    "fractional-second lease",
			req:     Request{Protocol: TCP, InternalPort: 22000, Lease: 1500 * time.Millisecond},
			wantErr: true,
		},
		{
			name:    "too large lease",
			req:     Request{Protocol: TCP, InternalPort: 22000, Lease: MaxLease + time.Second},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLeaseRenewBefore(t *testing.T) {
	acquired := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		lease Lease
		want  time.Time
	}{
		{
			name:  "half of one hour",
			lease: Lease{Lifetime: time.Hour, Acquired: acquired},
			want:  acquired.Add(30 * time.Minute),
		},
		{
			name:  "zero lifetime renews immediately",
			lease: Lease{Lifetime: 0, Acquired: acquired},
			want:  acquired,
		},
		{
			name:  "negative lifetime renews immediately",
			lease: Lease{Lifetime: -time.Second, Acquired: acquired},
			want:  acquired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.lease.RenewBefore())
		})
	}
}

func TestLeaseFields(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.7")
	acquired := time.Unix(100, 0)
	l := Lease{
		Protocol:     TCP,
		InternalPort: 22000,
		ExternalPort: 30000,
		ExternalIP:   ip,
		Lifetime:     time.Hour,
		Acquired:     acquired,
	}
	assert.Equal(t, TCP, l.Protocol)
	assert.Equal(t, uint16(22000), l.InternalPort)
	assert.Equal(t, uint16(30000), l.ExternalPort)
	assert.Equal(t, ip, l.ExternalIP)
	assert.Equal(t, time.Hour, l.Lifetime)
	assert.Equal(t, acquired, l.Acquired)
}
