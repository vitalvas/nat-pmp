// Package mapping defines the protocol-agnostic domain types used to describe
// desired port forwardings and the leases granted for them. These types are
// shared by every port-mapping protocol implementation and by the daemon, and
// carry no dependency on any concrete protocol.
package mapping

import (
	"fmt"
	"net/netip"
	"time"
)

// Protocol is the transport protocol of a port mapping.
type Protocol string

const (
	// TCP is the TCP transport protocol.
	TCP Protocol = "tcp"
	// UDP is the UDP transport protocol.
	UDP Protocol = "udp"
)

// MaxLease is the largest lifetime supported by the wire protocols. NAT-PMP
// and PCP encode lifetimes as unsigned 32-bit seconds.
const MaxLease = time.Duration(1<<32-1) * time.Second

// ParseProtocol converts a string into a Protocol, returning an error for any
// value other than "tcp" or "udp".
func ParseProtocol(s string) (Protocol, error) {
	switch Protocol(s) {
	case TCP:
		return TCP, nil
	case UDP:
		return UDP, nil
	default:
		return "", fmt.Errorf("invalid protocol %q: must be tcp or udp", s)
	}
}

// Valid reports whether the protocol is one of the supported values.
func (p Protocol) Valid() bool {
	return p == TCP || p == UDP
}

// Request describes a single desired port mapping.
type Request struct {
	// Protocol is the transport protocol (tcp or udp).
	Protocol Protocol
	// InternalPort is the port on this host that traffic is forwarded to.
	InternalPort uint16
	// ExternalPort is the requested port on the gateway's WAN side. A value of
	// zero asks the gateway to choose a port.
	ExternalPort uint16
	// Description is a human-readable label for the mapping.
	Description string
	// Lease is the requested lifetime of the mapping. A value of zero asks the
	// client to apply its default lifetime.
	Lease time.Duration
}

// Validate reports whether the request is well formed.
func (r Request) Validate() error {
	if !r.Protocol.Valid() {
		return fmt.Errorf("invalid protocol %q", r.Protocol)
	}
	if r.InternalPort == 0 {
		return fmt.Errorf("internal port must be non-zero")
	}
	if r.Lease < 0 {
		return fmt.Errorf("lease must not be negative")
	}
	if r.Lease > MaxLease {
		return fmt.Errorf("lease must not exceed %s", MaxLease)
	}
	if r.Lease > 0 && r.Lease%time.Second != 0 {
		return fmt.Errorf("lease must be a whole number of seconds")
	}
	return nil
}

// Lease is the result of a successfully created or refreshed mapping. The
// values reflect what the gateway actually granted, which may differ from what
// was requested.
type Lease struct {
	// Protocol is the transport protocol of the mapping.
	Protocol Protocol
	// InternalPort is the internal port the mapping forwards to.
	InternalPort uint16
	// ExternalPort is the external port the gateway actually assigned.
	ExternalPort uint16
	// ExternalIP is the gateway's WAN address at the time the lease was granted.
	ExternalIP netip.Addr
	// Lifetime is the lifetime the gateway actually granted.
	Lifetime time.Duration
	// Acquired is the local time at which the lease was obtained.
	Acquired time.Time
}

// RenewBefore returns the time at which the lease should be renewed, which is
// halfway through its granted lifetime. A non-positive lifetime yields the
// acquisition time so callers renew immediately.
func (l Lease) RenewBefore() time.Time {
	if l.Lifetime <= 0 {
		return l.Acquired
	}
	return l.Acquired.Add(l.Lifetime / 2)
}
