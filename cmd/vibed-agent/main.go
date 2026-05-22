// Command vibed-runner-agent is the in-pod agent that runs inside a pooled
// runner container. It exposes vibeD's control API for injecting user source
// and supervising the user process. See internal/runneragent.
//
// It is configured entirely from the environment so the runner image needs no
// arguments:
//
//	VIBED_AGENT_CONTROL_ADDR  control API listen address   (default ":9000")
//	VIBED_AGENT_WORKDIR       where user source is written  (default "/workspace")
//	VIBED_AGENT_TOKEN         bearer token for the control API (default: none)
//	VIBED_AGENT_APP_PORT      default port for the user app  (default 8080)
//	VIBED_AGENT_LOG_LINES     user-process log ring size     (default 500)
//
// The container image should run this under a minimal init (e.g. tini) so
// orphaned grandchildren of the user process are reaped.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := runneragent.Config{
		ControlAddr: env("VIBED_AGENT_CONTROL_ADDR", ":9000"),
		Workdir:     env("VIBED_AGENT_WORKDIR", "/workspace"),
		Token:       os.Getenv("VIBED_AGENT_TOKEN"),
		AppPort:     envInt("VIBED_AGENT_APP_PORT", 8080),
		LogLines:    envInt("VIBED_AGENT_LOG_LINES", 500),
		Logger:      logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := runneragent.New(cfg).Run(ctx); err != nil {
		logger.Error("runner agent exited with error", "error", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
