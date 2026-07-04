// Package cli builds and runs the nat-pmp command-line interface: it parses
// flags, loads configuration, sets up logging and signal handling, and runs the
// daemon.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vitalvas/nat-pmp/internal/config"
	"github.com/vitalvas/nat-pmp/internal/daemon"
)

// Execute runs the root command and returns a process exit code.
func Execute(ctx context.Context, version string) int {
	return executeCommand(ctx, newRootCommand(version))
}

func executeCommand(ctx context.Context, cmd *cobra.Command) int {
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// newRootCommand builds the root command that runs the daemon.
func newRootCommand(version string) *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:           "nat-pmp",
		Short:         "Maintain router port forwardings via UPnP, NAT-PMP, or PCP",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to the YAML configuration file")
	return cmd
}

// run loads the configuration, builds the daemon, and runs it until a signal
// arrives.
func run(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := daemon.New(cfg, logger)
	return d.Run(ctx)
}

// newLogger builds a slog text logger at the configured level.
func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)}))
}

// parseLevel maps a config level string to an slog.Level, defaulting to info.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
