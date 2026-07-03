// Package netgw discovers the default gateway (router) IPv4 address on Linux by
// parsing /proc/net/route. The gateway address is required by the NAT-PMP and
// PCP clients, which send their requests to the router directly.
package netgw

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const (
	routeFlagUp      = 0x1
	routeFlagGateway = 0x2
)

// routePath is the path to the Linux routing table. It is a variable rather
// than a constant so tests can point it at a fixture.
var routePath = "/proc/net/route"

// Default returns the default gateway IPv4 address by reading /proc/net/route.
func Default() (netip.Addr, error) {
	f, err := os.Open(routePath)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("netgw: open %s: %w", routePath, err)
	}
	defer f.Close()
	return parseDefaultGateway(f)
}

// parseDefaultGateway parses the routing table from r and returns the gateway of
// the default route (the row whose destination and mask are both zero).
func parseDefaultGateway(r io.Reader) (netip.Addr, error) {
	scanner := bufio.NewScanner(r)

	// Skip the header line.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return netip.Addr{}, fmt.Errorf("netgw: read route table: %w", err)
		}
		return netip.Addr{}, fmt.Errorf("netgw: empty route table")
	}

	var best netip.Addr
	var bestMetric uint64
	found := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Columns: Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if len(fields) < 8 {
			continue
		}
		if fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 16)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("netgw: parse route flags %q: %w", fields[3], err)
		}
		if flags&(routeFlagUp|routeFlagGateway) != routeFlagUp|routeFlagGateway {
			continue
		}
		metric, err := strconv.ParseUint(fields[6], 10, 32)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("netgw: parse route metric %q: %w", fields[6], err)
		}
		addr, err := parseHexAddr(fields[2])
		if err != nil {
			return netip.Addr{}, err
		}
		if addr.IsUnspecified() {
			continue
		}
		if !found || metric < bestMetric {
			best = addr
			bestMetric = metric
			found = true
		}
	}

	if err := scanner.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("netgw: read route table: %w", err)
	}
	if found {
		return best, nil
	}
	return netip.Addr{}, fmt.Errorf("netgw: no default route found")
}

// parseHexAddr converts an 8-character little-endian hex address (as stored in
// /proc/net/route) into a netip.Addr.
func parseHexAddr(hex string) (netip.Addr, error) {
	if len(hex) != 8 {
		return netip.Addr{}, fmt.Errorf("netgw: parse gateway %q: expected 8 hex characters", hex)
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("netgw: parse gateway %q: %w", hex, err)
	}
	// The value is stored in network (little-endian) byte order on disk; convert
	// to the four octets.
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	return netip.AddrFrom4(b), nil
}
