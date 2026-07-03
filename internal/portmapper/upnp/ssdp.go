package upnp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ssdpMulticast is the SSDP multicast address and port.
var ssdpMulticast = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

// searchType is the ST header value used to find Internet Gateway Devices.
const searchType = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"

// defaultSearchTimeout bounds how long discovery waits for responses.
const defaultSearchTimeout = 2 * time.Second

// ssdpResult is a single SSDP search response.
type ssdpResult struct {
	// Location is the URL of the device descriptor.
	Location string
}

// searchFunc performs SSDP discovery and returns the descriptor locations found.
type searchFunc func(ctx context.Context) ([]ssdpResult, error)

type ssdpConn interface {
	datagramReader
	WriteToUDP(b []byte, addr *net.UDPAddr) (int, error)
	SetReadDeadline(t time.Time) error
}

// discover sends an SSDP M-SEARCH and collects descriptor locations until the
// context is done or the search timeout elapses.
func discover(ctx context.Context, timeout time.Duration) ([]ssdpResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("upnp: ssdp listen: %w", err)
	}
	defer conn.Close()

	return discoverWithConn(ctx, timeout, conn)
}

func discoverWithConn(ctx context.Context, timeout time.Duration, conn ssdpConn) ([]ssdpResult, error) {
	msg := buildSearchRequest(timeout)
	n, err := conn.WriteToUDP(msg, ssdpMulticast)
	if err != nil {
		return nil, fmt.Errorf("upnp: ssdp write: %w", err)
	}
	if n != len(msg) {
		return nil, fmt.Errorf("upnp: ssdp short write: %d of %d bytes", n, len(msg))
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("upnp: ssdp deadline: %w", err)
	}

	return collectResults(ctx, conn)
}

// datagramReader reads one datagram at a time, returning a timeout error to end
// the collection window. *net.UDPConn satisfies this via ReadFromUDP.
type datagramReader interface {
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
}

// collectResults reads SSDP responses until the reader times out or the context
// is cancelled, returning the deduplicated descriptor locations found.
func collectResults(ctx context.Context, r datagramReader) ([]ssdpResult, error) {
	seen := make(map[string]struct{})
	var results []ssdpResult
	buf := make([]byte, 2048)
	for {
		if err := ctx.Err(); err != nil {
			if len(results) > 0 {
				return results, nil
			}
			return nil, err
		}
		n, _, err := r.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				// Timeout ends the collection window.
				break
			}
			return nil, fmt.Errorf("upnp: ssdp read: %w", err)
		}
		loc, err := parseLocation(buf[:n])
		if err != nil || loc == "" {
			continue
		}
		if _, dup := seen[loc]; dup {
			continue
		}
		seen[loc] = struct{}{}
		results = append(results, ssdpResult{Location: loc})
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("upnp: no SSDP responses")
	}
	return results, nil
}

func isTimeout(err error) bool {
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// buildSearchRequest builds an SSDP M-SEARCH datagram. The MX field is the
// maximum wait in seconds the devices may use before responding.
func buildSearchRequest(timeout time.Duration) []byte {
	mx := max(int(timeout.Seconds()), 1)
	var b bytes.Buffer
	b.WriteString("M-SEARCH * HTTP/1.1\r\n")
	b.WriteString("HOST: 239.255.255.250:1900\r\n")
	b.WriteString("MAN: \"ssdp:discover\"\r\n")
	fmt.Fprintf(&b, "MX: %d\r\n", mx)
	fmt.Fprintf(&b, "ST: %s\r\n", searchType)
	b.WriteString("\r\n")
	return b.Bytes()
}

// parseLocation extracts the LOCATION header from an SSDP response datagram.
func parseLocation(datagram []byte) (string, error) {
	reader := bufio.NewReader(bytes.NewReader(datagram))
	// SSDP responses are HTTP-like: a status line followed by headers.
	if _, err := reader.ReadString('\n'); err != nil {
		return "", err
	}
	tp := http.Header{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := splitHeader(line)
		if !ok {
			continue
		}
		tp.Add(key, value)
	}
	return tp.Get("Location"), nil
}

// splitHeader splits a "Key: Value" header line, trimming leading whitespace
// from the value.
func splitHeader(line string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return key, strings.TrimLeft(value, " \t"), true
}
