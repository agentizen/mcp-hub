package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "/etc/mcp-hub/config.yaml", "path to config.yaml")
	validate := flag.Bool("validate", false, "validate config and exit")
	portFlag := flag.Int("port", DefaultHTTPPort, "HTTP listen port")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	if *validate {
		logger.Info("config valid", "handles", len(cfg.Handles))
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, *portFlag, logger); err != nil {
		logger.Error("server exit", "err", err)
		os.Exit(1)
	}
}

// run is the testable entry point. It starts the HTTP server and the
// sweeper, waits for ctx to be canceled or for the server to fail, then
// drains subprocesses gracefully.
func run(ctx context.Context, cfg *Config, port int, logger *slog.Logger) error {
	pool := NewPool(cfg.Subprocesses, logger)
	dispatcher := NewDispatcher(cfg, pool, logger)

	sweepCtx, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	go pool.RunSweeper(sweepCtx)

	mux := http.NewServeMux()
	mux.Handle("/mcp/", dispatcher)
	mux.HandleFunc("/health", NewHealthHandler(cfg))

	server := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", server.Addr, "handles", len(cfg.Handles))
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		cancelSweep()
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}

	poolShutdownCtx, poolCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer poolCancel()
	pool.Shutdown(poolShutdownCtx)

	logger.Info("bye")
	return nil
}
