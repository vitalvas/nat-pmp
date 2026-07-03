package daemon

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/vitalvas/nat-pmp/internal/config"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
	"github.com/vitalvas/nat-pmp/internal/portmapper/natpmp"
	"github.com/vitalvas/nat-pmp/internal/portmapper/pcp"
	"github.com/vitalvas/nat-pmp/internal/portmapper/upnp"
)

// candidate pairs a protocol name with a factory that builds its client.
type candidate struct {
	name    string
	newFunc func(gateway netip.Addr) portmapper.Client
}

// defaultCandidates returns the protocol clients to probe, in the default
// preference order: NAT-PMP and PCP (fast UDP probes) before UPnP (slower SSDP).
func defaultCandidates() []candidate {
	return []candidate{
		{name: config.ProtocolNATPMP, newFunc: func(gw netip.Addr) portmapper.Client { return natpmp.New(gw) }},
		{name: config.ProtocolPCP, newFunc: func(gw netip.Addr) portmapper.Client { return pcp.New(gw) }},
		{name: config.ProtocolUPnP, newFunc: func(netip.Addr) portmapper.Client { return upnp.New() }},
	}
}

// orderCandidates returns the candidates reordered per the given protocol name
// list. Names not present are appended in their default position; unknown names
// are ignored.
func orderCandidates(all []candidate, order []string) []candidate {
	if len(order) == 0 {
		return all
	}
	byName := make(map[string]candidate, len(all))
	for _, c := range all {
		byName[c.name] = c
	}
	var ordered []candidate
	used := make(map[string]bool)
	for _, name := range order {
		if c, ok := byName[name]; ok && !used[name] {
			ordered = append(ordered, c)
			used[name] = true
		}
	}
	for _, c := range all {
		if !used[c.name] {
			ordered = append(ordered, c)
		}
	}
	return ordered
}

// detect probes each candidate in order and returns the first client whose
// gateway answers. Each probe is bounded by timeout.
func detect(ctx context.Context, gateway netip.Addr, candidates []candidate, timeout time.Duration) (portmapper.Client, error) {
	for _, c := range candidates {
		client := c.newFunc(gateway)
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		_, err := client.ExternalIP(probeCtx)
		cancel()
		if err == nil {
			return client, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("daemon: no supported port-mapping protocol found on gateway %s", gateway)
}
