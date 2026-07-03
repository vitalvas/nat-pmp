// Package natpmp implements a NAT-PMP (RFC 6886) client for requesting port
// mappings from a gateway. This file holds the pure wire codec: it encodes
// request messages and decodes response messages without performing any I/O.
package natpmp

import (
	"encoding/binary"
	"fmt"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

// protoVersion is the NAT-PMP protocol version (RFC 6886 uses 0).
const protoVersion = 0

// Opcodes for requests. Response opcodes are the request opcode plus 128.
const (
	opExternalAddress = 0
	opMapUDP          = 1
	opMapTCP          = 2
	opResponseFlag    = 128
)

// Result codes returned by the gateway (RFC 6886 section 3.5). Code 3
// (network failure) has no dedicated sentinel and falls through to a generic
// error, so it is not named here.
const (
	resultSuccess        = 0
	resultUnsupportedVer = 1
	resultNotAuthorized  = 2
	resultOutOfResources = 4
	resultUnsupportedOp  = 5
)

// resultError maps a NAT-PMP result code to a sentinel error, returning nil for
// success.
func resultError(code uint16) error {
	switch code {
	case resultSuccess:
		return nil
	case resultNotAuthorized:
		return portmapper.ErrNotAuthorized
	case resultOutOfResources:
		return portmapper.ErrNoResources
	case resultUnsupportedVer, resultUnsupportedOp:
		return portmapper.ErrUnsupported
	default:
		return fmt.Errorf("natpmp: gateway result code %d", code)
	}
}

// mapOpcode returns the request opcode for the given transport protocol.
func mapOpcode(p mapping.Protocol) (byte, error) {
	switch p {
	case mapping.UDP:
		return opMapUDP, nil
	case mapping.TCP:
		return opMapTCP, nil
	default:
		return 0, fmt.Errorf("natpmp: unsupported protocol %q", p)
	}
}

// encodeExternalAddressRequest builds a 2-byte external address request.
func encodeExternalAddressRequest() []byte {
	return []byte{protoVersion, opExternalAddress}
}

// externalAddressResponse is the decoded form of an external address response.
type externalAddressResponse struct {
	Epoch      uint32
	ExternalIP [4]byte
}

// decodeExternalAddressResponse parses a 12-byte external address response.
func decodeExternalAddressResponse(b []byte) (externalAddressResponse, error) {
	if len(b) < 12 {
		return externalAddressResponse{}, fmt.Errorf("natpmp: external address response too short: %d bytes", len(b))
	}
	if b[0] != protoVersion {
		return externalAddressResponse{}, fmt.Errorf("natpmp: unexpected version %d", b[0])
	}
	if b[1] != opExternalAddress+opResponseFlag {
		return externalAddressResponse{}, fmt.Errorf("natpmp: unexpected opcode %d", b[1])
	}
	code := binary.BigEndian.Uint16(b[2:4])
	if err := resultError(code); err != nil {
		return externalAddressResponse{}, err
	}
	var resp externalAddressResponse
	resp.Epoch = binary.BigEndian.Uint32(b[4:8])
	copy(resp.ExternalIP[:], b[8:12])
	return resp, nil
}

// encodeMapRequest builds a 12-byte port mapping request. A lifetime of zero
// requests deletion of the mapping.
func encodeMapRequest(p mapping.Protocol, internalPort, externalPort uint16, lifetimeSec uint32) ([]byte, error) {
	op, err := mapOpcode(p)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 12)
	b[0] = protoVersion
	b[1] = op
	// b[2:4] reserved, left zero.
	binary.BigEndian.PutUint16(b[4:6], internalPort)
	binary.BigEndian.PutUint16(b[6:8], externalPort)
	binary.BigEndian.PutUint32(b[8:12], lifetimeSec)
	return b, nil
}

// mapResponse is the decoded form of a port mapping response.
type mapResponse struct {
	Epoch        uint32
	InternalPort uint16
	ExternalPort uint16
	LifetimeSec  uint32
}

// decodeMapResponse parses a 16-byte port mapping response, validating that its
// opcode matches the requested protocol.
func decodeMapResponse(b []byte, p mapping.Protocol) (mapResponse, error) {
	if len(b) < 16 {
		return mapResponse{}, fmt.Errorf("natpmp: map response too short: %d bytes", len(b))
	}
	if b[0] != protoVersion {
		return mapResponse{}, fmt.Errorf("natpmp: unexpected version %d", b[0])
	}
	op, err := mapOpcode(p)
	if err != nil {
		return mapResponse{}, err
	}
	if b[1] != op+opResponseFlag {
		return mapResponse{}, fmt.Errorf("natpmp: unexpected opcode %d", b[1])
	}
	code := binary.BigEndian.Uint16(b[2:4])
	if err := resultError(code); err != nil {
		return mapResponse{}, err
	}
	return mapResponse{
		Epoch:        binary.BigEndian.Uint32(b[4:8]),
		InternalPort: binary.BigEndian.Uint16(b[8:10]),
		ExternalPort: binary.BigEndian.Uint16(b[10:12]),
		LifetimeSec:  binary.BigEndian.Uint32(b[12:16]),
	}, nil
}
