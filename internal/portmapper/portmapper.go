// Package portmapper defines the Client interface that every port-mapping
// protocol (NAT-PMP, PCP, UPnP IGD) implements, along with the set of sentinel
// errors those implementations translate their native result codes into. The
// daemon depends only on this interface and never on a concrete protocol.
package portmapper

import (
	"context"
	"errors"
	"net/netip"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// Sentinel errors that protocol implementations return so that callers can
// react to failure conditions without knowing which protocol produced them.
var (
	// ErrUnsupported indicates the gateway does not speak the protocol. It is
	// used during protocol auto-detection.
	ErrUnsupported = errors.New("portmapper: protocol not supported by gateway")
	// ErrConflict indicates the requested external port is already mapped by a
	// different host.
	ErrConflict = errors.New("portmapper: external port in use by another host")
	// ErrNoResources indicates the gateway has no free mapping resources.
	ErrNoResources = errors.New("portmapper: gateway out of mapping resources")
	// ErrNotAuthorized indicates the gateway refused to create the mapping.
	ErrNotAuthorized = errors.New("portmapper: gateway refused mapping")
)

// Client requests and releases port mappings on a gateway using a single
// port-mapping protocol.
type Client interface {
	// ExternalIP returns the gateway's current WAN address. It also serves as
	// the probe used during protocol detection: an error means the gateway does
	// not answer this protocol.
	ExternalIP(ctx context.Context) (netip.Addr, error)

	// Map creates or refreshes a single mapping and returns the granted lease.
	// The returned lease reflects what the gateway actually assigned, which may
	// differ from the request.
	Map(ctx context.Context, req mapping.Request) (mapping.Lease, error)

	// Unmap releases a single mapping. It is idempotent: releasing a mapping
	// that does not exist is not an error.
	Unmap(ctx context.Context, req mapping.Request) error

	// Name identifies the protocol for logging.
	Name() string
}
