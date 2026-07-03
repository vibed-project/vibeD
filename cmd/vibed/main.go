package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/server"
)

func main() {
	// Subcommands (must be handled before flag.Parse, which can't see them).
	if len(os.Args) > 1 && os.Args[1] == "validate-images" {
		os.Exit(runValidateImages(os.Args[2:]))
	}

	var (
		configPath string
		transport  string
	)
	flag.StringVar(&configPath, "config", "", "Path to vibed.yaml config file")
	flag.StringVar(&transport, "transport", "", "Override transport: stdio, http, or both")
	flag.Parse()

	// Bootstrap logger for config loading (always text, info level).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if transport != "" {
		cfg.Server.Transport = transport
	}

	// Replace the bootstrap logger with the configured format and level, then
	// hand off to the shared server wiring (reused by out-of-tree editions).
	logger = server.NewLogger(cfg.Server)
	server.Run(cfg, logger)
}
