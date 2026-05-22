// Command vibed-workerd-loader is the sidecar that runs alongside a workerd
// process in the fast-lane StatefulSet. It owns the set of deployed worker
// scripts: vibed-controller POSTs a deploy (app id + source URL), the loader
// fetches the tarball, extracts the worker module, writes it to the shared
// scripts volume, re-renders workerd's config.capnp, and reloads workerd.
//
// See internal/workerd for the loader core and the config-schema caveat.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/vibed-project/vibeD/internal/workerd"
)

func main() {
	var (
		listenAddr string
		baseDir    string
		compatDate string
		portBase   int
		reloadCmd  string
		token      string
	)
	flag.StringVar(&listenAddr, "listen", ":9200", "Control API listen address.")
	flag.StringVar(&baseDir, "base-dir", "/var/lib/workerd", "Dir for scripts/ and config.capnp (shared with the workerd container).")
	flag.StringVar(&compatDate, "compat-date", "2024-01-01", "workerd compatibilityDate applied to every worker.")
	flag.IntVar(&portBase, "port-base", 9100, "First port assigned to app sockets.")
	flag.StringVar(&reloadCmd, "reload-cmd", "", "Shell command run after each config change to reload workerd (e.g. send SIGHUP). Empty = no-op (workerd auto-watches its config).")
	flag.StringVar(&token, "token", os.Getenv("VIBED_AGENT_TOKEN"), "Bearer token required on the control API. Defaults to $VIBED_AGENT_TOKEN.")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	loader, err := workerd.New(workerd.Options{
		BaseDir:    baseDir,
		CompatDate: compatDate,
		PortBase:   portBase,
		Reload:     reloadFunc(reloadCmd, logger),
	})
	if err != nil {
		logger.Error("loader init failed", "error", err)
		os.Exit(1)
	}

	handler := workerd.NewHandler(loader, token)
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting vibed-workerd-loader", "listen", listenAddr, "baseDir", baseDir, "configPath", loader.ConfigPath())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// reloadFunc returns the reload hook the loader calls after each config
// change. When reloadCmd is empty we no-op (workerd can be run with config
// auto-watch); otherwise we run the command via sh -c.
func reloadFunc(reloadCmd string, logger *slog.Logger) func() error {
	if reloadCmd == "" {
		return func() error { return nil }
	}
	return func() error {
		out, err := exec.Command("sh", "-c", reloadCmd).CombinedOutput()
		if err != nil {
			logger.Error("workerd reload failed", "cmd", reloadCmd, "output", string(out), "error", err)
			return err
		}
		return nil
	}
}
