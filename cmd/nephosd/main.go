// Command nephosd is the Nephos control plane. It runs inside the appliance
// container and owns the API server, the store, the reconcilers, and the
// network and compute engines.
//
// M1 begins with authenticated health and version routes. Resource handlers,
// the store, and reconcilers are added in later M1 slices.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amrzxk/nephos/internal/apiserver"
	"github.com/Amrzxk/nephos/internal/credentials"
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

	// context.Background() belongs only in main and tests (AGENTS.md).
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

	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(ctx, "tcp", ":7788")
	if err != nil {
		return fmt.Errorf("listening for the API: %w", err)
	}
	defer ln.Close()
	if err := serve(ctx, ln, "/var/lib/nephos/secrets/api-token", info); err != nil {
		return err
	}
	logger.Info("nephosd shutting down", slog.String("reason", context.Cause(ctx).Error()))
	return nil
}

func serve(ctx context.Context, ln net.Listener, tokenPath string, build version.Info) error {
	token, err := credentials.LoadOrCreate(tokenPath)
	if err != nil {
		return fmt.Errorf("loading API token: %w", err)
	}
	h := apiserver.New(token, build, func() bool { return true })
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case <-stopped:
		}
	}()
	err = srv.Serve(ln)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("serving the API: %w", err)
	}
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
