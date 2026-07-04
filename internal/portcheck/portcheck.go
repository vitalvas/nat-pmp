// Package portcheck reports whether a local TCP or UDP port has a listener on
// Linux by parsing /proc/net/{tcp,tcp6,udp,udp6}. The daemon uses it to gate
// port mappings on the presence of a real listener, so a forwarding is only
// created while something is actually accepting traffic on the internal port.
package portcheck

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

// tcpListenState is the value the kernel records in the "st" column of
// /proc/net/tcp for a socket in the LISTEN state.
const tcpListenState = "0A"

// procPaths maps a transport protocol to the /proc files that list its sockets.
// They are variables rather than constants so tests can point them at fixtures.
var (
	tcpPaths = []string{"/proc/net/tcp", "/proc/net/tcp6"}
	udpPaths = []string{"/proc/net/udp", "/proc/net/udp6"}
)

// HasListener reports whether the given protocol has a socket bound to port on
// this host. For TCP a socket must be in the LISTEN state; UDP sockets have no
// listen state, so any socket bound to the port counts.
func HasListener(protocol mapping.Protocol, port uint16) (bool, error) {
	var paths []string
	switch protocol {
	case mapping.TCP:
		paths = tcpPaths
	case mapping.UDP:
		paths = udpPaths
	default:
		return false, fmt.Errorf("portcheck: invalid protocol %q", protocol)
	}

	tcpOnly := protocol == mapping.TCP
	for _, path := range paths {
		found, err := scanPath(path, port, tcpOnly)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// scanPath reports whether any socket in the /proc file at path is bound to
// port. A missing file is treated as "no such socket" so that a host lacking,
// for example, IPv6 does not produce an error.
func scanPath(path string, port uint16, requireListen bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("portcheck: open %s: %w", path, err)
	}
	defer f.Close()
	return scan(f, port, requireListen)
}

// scan parses the socket table from r and reports whether a socket is bound to
// port, honoring requireListen for TCP.
func scan(r io.Reader, port uint16, requireListen bool) (bool, error) {
	scanner := bufio.NewScanner(r)

	// Skip the header line.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("portcheck: read socket table: %w", err)
		}
		return false, nil
	}

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Columns: sl local_address rem_address st tx_queue:rx_queue ...
		if len(fields) < 4 {
			continue
		}
		localPort, ok := parseLocalPort(fields[1])
		if !ok || localPort != port {
			continue
		}
		if requireListen && fields[3] != tcpListenState {
			continue
		}
		return true, nil
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("portcheck: read socket table: %w", err)
	}
	return false, nil
}

// parseLocalPort extracts the port from a "HEXADDR:HEXPORT" local_address field.
// The port is a big-endian 16-bit hex value.
func parseLocalPort(localAddr string) (uint16, bool) {
	colon := strings.LastIndexByte(localAddr, ':')
	if colon < 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(localAddr[colon+1:], 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}
