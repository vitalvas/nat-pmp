package daemon

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/vitalvas/nat-pmp/internal/config"
	"github.com/vitalvas/nat-pmp/internal/netgw"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

// defaultGateway discovers the LAN gateway via the OS routing table.
func defaultGateway() (netip.Addr, error) {
	return netgw.Default()
}

// defaultDetect probes the default set of protocol candidates, honoring any
// detection order and timeout configured on the daemon.
func (d *Daemon) defaultDetect(ctx context.Context, gateway netip.Addr) (portmapper.Client, error) {
	candidates := orderCandidates(defaultCandidates(), d.cfg.Detect.Order)
	timeout := d.cfg.Detect.Timeout
	if timeout == 0 {
		timeout = config.DefaultDetectTimeout
	}
	return detect(ctx, gateway, candidates, timeout)
}

// localAddrFor returns this host's LAN address on the route toward the gateway.
// It opens a UDP socket to the gateway (no packets are sent) and reads the local
// address the kernel selected.
func localAddrFor(gateway netip.Addr) (netip.Addr, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(gateway.String(), "9"))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("daemon: determine local address: %w", err)
	}
	defer conn.Close()

	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("daemon: invalid local address")
	}
	local, ok := netip.AddrFromSlice(udpAddr.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("daemon: invalid local address")
	}
	return local.Unmap(), nil
}
