// Package pcp implements a PCP (RFC 6887) client for requesting port mappings
// from a gateway using the MAP opcode. This file holds the pure wire codec: it
// encodes MAP requests and decodes MAP responses without performing any I/O.
package pcp

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

// protoVersion is the PCP protocol version (RFC 6887 uses 2).
const protoVersion = 2

// opMap is the MAP opcode. The response flag is the high bit of the opcode byte.
const (
	opMap          = 1
	responseFlag   = 0x80
	nonceLen       = 12
	requestHdrLen  = 24                           // common request header
	mapOpDataLen   = 36                           // nonce(12)+proto(1)+reserved(3)+iport(2)+eport(2)+eaddr(16)
	requestLen     = requestHdrLen + mapOpDataLen // 60
	responseHdrLen = 24
	responseLen    = responseHdrLen + mapOpDataLen // 60
)

// IANA transport protocol numbers used in the MAP opcode.
const (
	ipProtoTCP = 6
	ipProtoUDP = 17
)

// Result codes (RFC 6887 section 7.4). Codes without a dedicated sentinel
// (such as 3, malformed request) fall through to a generic error and are not
// named here.
const (
	resultSuccess       = 0
	resultUnsuppVersion = 1
	resultNotAuthorized = 2
	resultUnsuppOpcode  = 4
	resultNoResources   = 8
)

// resultError maps a PCP result code to a sentinel error, returning nil for
// success.
func resultError(code uint8) error {
	switch code {
	case resultSuccess:
		return nil
	case resultNotAuthorized:
		return portmapper.ErrNotAuthorized
	case resultNoResources:
		return portmapper.ErrNoResources
	case resultUnsuppVersion, resultUnsuppOpcode:
		return portmapper.ErrUnsupported
	default:
		return fmt.Errorf("pcp: gateway result code %d", code)
	}
}

// ipProtocol returns the IANA protocol number for a transport protocol.
func ipProtocol(p mapping.Protocol) (uint8, error) {
	switch p {
	case mapping.TCP:
		return ipProtoTCP, nil
	case mapping.UDP:
		return ipProtoUDP, nil
	default:
		return 0, fmt.Errorf("pcp: unsupported protocol %q", p)
	}
}

// to16 returns the 16-byte form of an address, mapping IPv4 into the
// IPv4-mapped IPv6 space as required by RFC 6887.
func to16(a netip.Addr) [16]byte {
	if a.Is4() {
		a = netip.AddrFrom16(a.As16()) // As16 already yields the v4-mapped form
	}
	return a.As16()
}

// mapRequest holds the fields needed to build a MAP request.
type mapRequest struct {
	Nonce        [nonceLen]byte
	Protocol     mapping.Protocol
	InternalPort uint16
	ExternalPort uint16
	ExternalAddr netip.Addr
	ClientAddr   netip.Addr
	LifetimeSec  uint32
}

// encodeMapRequest serializes a MAP request into its 60-byte wire form.
func encodeMapRequest(r mapRequest) ([]byte, error) {
	proto, err := ipProtocol(r.Protocol)
	if err != nil {
		return nil, err
	}
	if !r.ClientAddr.IsValid() {
		return nil, fmt.Errorf("pcp: invalid client address")
	}
	if !r.ExternalAddr.IsValid() {
		return nil, fmt.Errorf("pcp: invalid external address")
	}

	b := make([]byte, requestLen)
	b[0] = protoVersion
	b[1] = opMap // R bit is 0 for requests
	// b[2:4] reserved
	binary.BigEndian.PutUint32(b[4:8], r.LifetimeSec)
	client := to16(r.ClientAddr)
	copy(b[8:24], client[:])

	// MAP opcode-specific data starts at offset 24.
	copy(b[24:36], r.Nonce[:])
	b[36] = proto
	// b[37:40] reserved
	binary.BigEndian.PutUint16(b[40:42], r.InternalPort)
	binary.BigEndian.PutUint16(b[42:44], r.ExternalPort)
	ext := to16(r.ExternalAddr)
	copy(b[44:60], ext[:])
	return b, nil
}

// mapResponse is the decoded form of a MAP response.
type mapResponse struct {
	ResultCode   uint8
	LifetimeSec  uint32
	Epoch        uint32
	Nonce        [nonceLen]byte
	Protocol     uint8
	InternalPort uint16
	ExternalPort uint16
	ExternalAddr netip.Addr
}

// decodeMapResponse parses a 60-byte MAP response, validating the version,
// response flag, opcode and result code.
func decodeMapResponse(b []byte) (mapResponse, error) {
	if len(b) < responseLen {
		return mapResponse{}, fmt.Errorf("pcp: map response too short: %d bytes", len(b))
	}
	if b[0] != protoVersion {
		return mapResponse{}, fmt.Errorf("pcp: unexpected version %d", b[0])
	}
	if b[1] != opMap|responseFlag {
		return mapResponse{}, fmt.Errorf("pcp: unexpected opcode byte 0x%02x", b[1])
	}

	var resp mapResponse
	resp.ResultCode = b[3]
	if err := resultError(resp.ResultCode); err != nil {
		return mapResponse{}, err
	}
	resp.LifetimeSec = binary.BigEndian.Uint32(b[4:8])
	resp.Epoch = binary.BigEndian.Uint32(b[8:12])
	// b[12:24] reserved

	copy(resp.Nonce[:], b[24:36])
	resp.Protocol = b[36]
	resp.InternalPort = binary.BigEndian.Uint16(b[40:42])
	resp.ExternalPort = binary.BigEndian.Uint16(b[42:44])

	var ext [16]byte
	copy(ext[:], b[44:60])
	addr := netip.AddrFrom16(ext)
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	resp.ExternalAddr = addr
	return resp, nil
}
