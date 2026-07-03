package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

// ensure creates or refreshes a single mapping and returns the granted lease.
// It logs the outcome, including when the gateway assigns a different external
// port than requested.
func ensure(ctx context.Context, client portmapper.Client, log *slog.Logger, req mapping.Request) (mapping.Lease, error) {
	lease, err := client.Map(ctx, req)
	if err != nil {
		log.Warn("mapping failed",
			"protocol", req.Protocol,
			"internal_port", req.InternalPort,
			"external_port", req.ExternalPort,
			"error", err,
		)
		return mapping.Lease{}, err
	}
	if err := validateLease(req, lease); err != nil {
		log.Warn("gateway returned invalid lease",
			"protocol", req.Protocol,
			"internal_port", req.InternalPort,
			"external_port", req.ExternalPort,
			"error", err,
		)
		return mapping.Lease{}, err
	}

	if req.ExternalPort != 0 && lease.ExternalPort != req.ExternalPort {
		log.Warn("gateway assigned a different external port",
			"protocol", req.Protocol,
			"requested_external_port", req.ExternalPort,
			"assigned_external_port", lease.ExternalPort,
		)
	}

	log.Info("mapping active",
		"protocol", lease.Protocol,
		"internal_port", lease.InternalPort,
		"external_ip", lease.ExternalIP,
		"external_port", lease.ExternalPort,
		"lifetime", lease.Lifetime,
	)
	return lease, nil
}

func validateLease(req mapping.Request, lease mapping.Lease) error {
	if lease.Protocol != req.Protocol {
		return fmt.Errorf("lease protocol %q does not match request %q", lease.Protocol, req.Protocol)
	}
	if lease.InternalPort != req.InternalPort {
		return fmt.Errorf("lease internal port %d does not match request %d", lease.InternalPort, req.InternalPort)
	}
	if lease.ExternalPort == 0 {
		return fmt.Errorf("lease external port must be non-zero")
	}
	if lease.Lifetime <= 0 {
		return fmt.Errorf("lease lifetime must be positive")
	}
	return nil
}
