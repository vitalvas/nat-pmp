package portcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"

// tcpListenRow is a LISTEN socket on 0.0.0.0:22000 (0x55F0).
const tcpListenRow = "   0: 00000000:55F0 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0"

// tcpEstablishedRow is an ESTABLISHED (st 01) socket on 0.0.0.0:22000.
const tcpEstablishedRow = "   1: 00000000:55F0 0100007F:1234 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0"

// udpBoundRow is a UDP socket bound to 0.0.0.0:51820 (0xCA6C), state 07.
const udpBoundRow = "   0: 00000000:CA6C 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 22222 2 0000000000000000 0"

func writeFile(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := strings.Join(append(lines, ""), "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestHasListener(t *testing.T) {
	dir := t.TempDir()
	tcp4 := writeFile(t, dir, "tcp", tcpHeader, tcpListenRow, tcpEstablishedRow)
	udp4 := writeFile(t, dir, "udp", tcpHeader, udpBoundRow)
	missing := filepath.Join(dir, "does-not-exist")

	origTCP, origUDP := tcpPaths, udpPaths
	t.Cleanup(func() { tcpPaths, udpPaths = origTCP, origUDP })
	tcpPaths = []string{tcp4, missing}
	udpPaths = []string{udp4, missing}

	t.Run("tcp listening port found", func(t *testing.T) {
		got, err := HasListener(mapping.TCP, 22000)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("tcp non-listening port not found", func(t *testing.T) {
		got, err := HasListener(mapping.TCP, 40000)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("udp bound port found", func(t *testing.T) {
		got, err := HasListener(mapping.UDP, 51820)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("udp unbound port not found", func(t *testing.T) {
		got, err := HasListener(mapping.UDP, 22000)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("invalid protocol", func(t *testing.T) {
		_, err := HasListener(mapping.Protocol("sctp"), 1)
		require.Error(t, err)
	})
}

func TestHasListenerScanError(t *testing.T) {
	// Point a path at a directory so opening it fails with a non-"not exist"
	// error, which HasListener must surface.
	dir := t.TempDir()
	orig := tcpPaths
	t.Cleanup(func() { tcpPaths = orig })
	tcpPaths = []string{dir}

	_, err := HasListener(mapping.TCP, 80)
	require.Error(t, err)
}

func TestHasListenerEstablishedNotCountedForTCP(t *testing.T) {
	dir := t.TempDir()
	// Only an ESTABLISHED socket exists on the port; no LISTEN.
	tcp4 := writeFile(t, dir, "tcp", tcpHeader, tcpEstablishedRow)

	orig := tcpPaths
	t.Cleanup(func() { tcpPaths = orig })
	tcpPaths = []string{tcp4}

	got, err := HasListener(mapping.TCP, 22000)
	require.NoError(t, err)
	assert.False(t, got)
}

func TestScanPathOpenError(t *testing.T) {
	// A directory is not a missing file, so opening it must surface an error.
	dir := t.TempDir()
	_, err := scanPath(dir, 80, true)
	require.Error(t, err)
}

func TestScan(t *testing.T) {
	t.Run("empty reader", func(t *testing.T) {
		got, err := scan(strings.NewReader(""), 80, true)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("header only", func(t *testing.T) {
		got, err := scan(strings.NewReader(fmt.Sprintf("%s\n", tcpHeader)), 80, true)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("short rows skipped", func(t *testing.T) {
		got, err := scan(strings.NewReader(fmt.Sprintf("%s\n0: 00000000:0050\n", tcpHeader)), 80, true)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("malformed local address skipped", func(t *testing.T) {
		row := "   0: notaddr 00000000:0000 0A a b c d e f"
		got, err := scan(strings.NewReader(fmt.Sprintf("%s\n%s\n", tcpHeader, row)), 80, true)
		require.NoError(t, err)
		assert.False(t, got)
	})
}

func TestParseLocalPort(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		port, ok := parseLocalPort("0100007F:0050")
		require.True(t, ok)
		assert.Equal(t, uint16(80), port)
	})

	t.Run("no colon", func(t *testing.T) {
		_, ok := parseLocalPort("0100007F")
		assert.False(t, ok)
	})

	t.Run("bad hex", func(t *testing.T) {
		_, ok := parseLocalPort("0100007F:zzzz")
		assert.False(t, ok)
	})
}
