package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{in: "debug", want: slog.LevelDebug},
		{in: "DEBUG", want: slog.LevelDebug},
		{in: "info", want: slog.LevelInfo},
		{in: "warn", want: slog.LevelWarn},
		{in: "warning", want: slog.LevelWarn},
		{in: "error", want: slog.LevelError},
		{in: " Error ", want: slog.LevelError},
		{in: "", want: slog.LevelInfo},
		{in: "bogus", want: slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, parseLevel(tt.in))
		})
	}
}

func TestNewLogger(t *testing.T) {
	logger := newLogger("debug")
	require.NotNil(t, logger)
	assert.True(t, logger.Enabled(context.Background(), slog.LevelDebug))
}

func TestNewRootCommand(t *testing.T) {
	cmd := newRootCommand()
	assert.Equal(t, "nat-pmp", cmd.Use)
	assert.NotNil(t, cmd.Args)
	flag := cmd.Flags().Lookup("config")
	require.NotNil(t, flag)
	assert.Equal(t, "config.yaml", flag.DefValue)
}

func TestRunConfigLoadError(t *testing.T) {
	// Missing/empty config -> load fails validation, run returns an error before
	// touching the network.
	err := run(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"))
	require.Error(t, err)
}

func TestRunGatewayFailureSurfaces(t *testing.T) {
	// A valid config but no reachable gateway: on this host /proc/net/route is
	// absent (non-Linux) or discovery/detection fails. Either way run returns an
	// error rather than hanging, because ctx is cancelled up front.
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "mappings:\n  - protocol: tcp\n    internal_port: 80\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so detection cannot block indefinitely

	err := run(ctx, path)
	require.Error(t, err)
}

func TestExecuteReturnsErrorCode(t *testing.T) {
	t.Run("command error surfaces", func(t *testing.T) {
		cmd := newRootCommand()
		cmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "absent.yaml")})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
	})

	t.Run("extra positional args rejected", func(t *testing.T) {
		cmd := newRootCommand()
		cmd.SetArgs([]string{"unexpected"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
	})

	t.Run("execute returns non-zero on error", func(t *testing.T) {
		// With no config.yaml in the working directory, config load fails and
		// Execute reports a non-zero exit code.
		t.Chdir(t.TempDir())
		code := Execute(context.Background())
		assert.Equal(t, 1, code)
	})
}

func TestExecuteCommandSuccessCode(t *testing.T) {
	cmd := &cobra.Command{
		Use: "ok",
		RunE: func(*cobra.Command, []string) error {
			return nil
		},
	}
	assert.Equal(t, 0, executeCommand(context.Background(), cmd))
}
