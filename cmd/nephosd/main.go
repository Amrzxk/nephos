// Command nephosd is the Nephos control plane. It runs inside the appliance
// container and owns the API server, the store, the reconcilers, and the
// network and compute engines.
//
// M0 scope: this binary establishes the logging convention (structured
// log/slog, ADR-0002) and the signal-handling shape that the real server will
// use. The API server, store, and reconcilers arrive in M1.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Amrzxk/nephos/internal/version"
)

func main() {
	// os.Exit skips deferred calls, so the whole process body lives in
	// realMain and main does nothing but translate its result.
	os.Exit(realMain())
}

func realMain() int {
	logger := newLogger(os.Getenv("NEPHOS_LOG_LEVEL"))
	slog.SetDefault(logger)

	// context.Background() belongs only in main and tests (CLAUDE.md).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("nephosd exited with an error", slog.Any("error", err))
		return 1
	}
	return 0
}

func run(ctx context.Context, logger *slog.Logger) error {
	info := version.Get()
	logger.Info("nephosd starting",
		slog.String("version", info.Version),
		slog.String("commit", info.Commit),
		slog.String("go_version", info.GoVersion),
		slog.String("platform", info.Platform),
	)

	// M1 wires the store, reconcilers, and API server here. Until then the
	// process starts, reports its identity, and waits to be told to stop, so
	// the appliance image and its signal handling can be exercised end to end.
	logger.Warn("no subsystems are wired yet; this is the M0 scaffold (see docs/ROADMAP.md)")

	<-ctx.Done()
	logger.Info("nephosd shutting down", slog.String("reason", context.Cause(ctx).Error()))
	return nil
}

// newLogger builds the structured JSON logger every Nephos component uses.
// Text output is deliberately not offered: the CLI pretty-prints the JSON
// stream (`nephos logs`), so there is one wire format.
func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug", "DEBUG":
		level = slog.LevelDebug
	case "warn", "WARN":
		level = slog.LevelWarn
	case "error", "ERROR":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
