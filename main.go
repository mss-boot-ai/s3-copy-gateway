package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mss-boot-ai/s3-copy-gateway/internal/copygateway"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if isVersionCommand(os.Args[1:]) {
		fmt.Println(versionString(version, commit, buildDate))
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("s3 copy gateway stopped with error", "error", err)
		os.Exit(1)
	}
}

func isVersionCommand(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "version", "-version", "--version":
		return true
	default:
		return false
	}
}

func versionString(releaseVersion, releaseCommit, releaseBuildDate string) string {
	return fmt.Sprintf("s3-copy-gateway version=%s commit=%s build_date=%s", releaseVersion, releaseCommit, releaseBuildDate)
}

func run(logger *slog.Logger) error {
	cfg, err := copygateway.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	source, err := copygateway.NewS3Source(cfg.Source, cfg.MaxInFlight)
	if err != nil {
		return fmt.Errorf("initialize source S3 client: %w", err)
	}
	target, err := copygateway.NewS3CopyTarget(cfg.Target, cfg.MaxInFlight)
	if err != nil {
		return fmt.Errorf("initialize target S3 client: %w", err)
	}
	handler, err := copygateway.NewServer(cfg, source, target, logger)
	if err != nil {
		return fmt.Errorf("initialize server: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		logger.Info("s3 copy gateway started", "listen_addr", cfg.ListenAddr, "max_in_flight", cfg.MaxInFlight)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case serveErr := <-errCh:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.CopyTimeout+5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}
	logger.Info("s3 copy gateway stopped")
	return nil
}
