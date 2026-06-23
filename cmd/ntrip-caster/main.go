// Command ntrip-caster is an NTRIP (v1/v2) caster with handover endpoints and
// hot-reloadable configuration.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/symysak/ntrip-caster/internal/caster"
	"github.com/symysak/ntrip-caster/internal/config"
	"github.com/symysak/ntrip-caster/internal/server"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "ntrip-caster/dev"

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "path to the YAML configuration file")
		logLevel   = flag.String("log-level", "info", "log level: debug, info, warn, error")
		check      = flag.Bool("check", false, "validate the configuration and exit")
	)
	flag.Parse()

	logger := newLogger(*logLevel)
	logger.Info("ntrip-caster starting", "version", version, "config", *configPath, "log-level", *logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if *check {
		fmt.Println("configuration OK")
		return
	}

	if err := run(logger, cfg, *configPath); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, cfg *config.Config, configPath string) error {
	mgr := caster.New(cfg, logger)
	srv := server.New(mgr, logger, version)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	logger.Info("ntrip-caster started", "listen", cfg.Listen, "version", version,
		"mountpoints", len(cfg.Mountpoints), "handover", len(cfg.Handover))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// SIGHUP triggers a hot reload of users, mountpoints, and base metadata.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			newCfg, err := config.Load(configPath)
			if err != nil {
				logger.Error("reload failed; keeping current config", "error", err)
				continue
			}
			if newCfg.Listen != mgr.Config().Listen {
				logger.Warn("listen address change ignored on reload; restart required",
					"current", mgr.Config().Listen, "new", newCfg.Listen)
			}
			mgr.Reload(newCfg)
		}
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		ln.Close()
		return nil
	case err := <-serveErr:
		return err
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
