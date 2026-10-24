// Command llm-proxy runs the multi-protocol LLM gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	llmproxy "github.com/ratsdev/llm-proxy"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("llm-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	var (
		configPath  string
		host        string
		port        int
		logLevel    string
		showVersion bool
	)
	flags.StringVar(&configPath, "config", "", "path to the TOML configuration file")
	flags.StringVar(&configPath, "c", "", "path to the TOML configuration file (shorthand)")
	flags.StringVar(&host, "host", "", "listen address (overrides server.host)")
	flags.IntVar(&port, "port", 0, "listen port (overrides server.port)")
	flags.StringVar(&logLevel, "log-level", "", "log level: debug, info, warning, error")
	flags.BoolVar(&showVersion, "version", false, "print the version and exit")

	if err := flags.Parse(args); err != nil {
		return 2
	}
	if showVersion {
		fmt.Printf("llm-proxy %s\n", llmproxy.Version)
		return 0
	}
	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintln(os.Stderr, "llm-proxy: --config is required")
		flags.Usage()
		return 2
	}

	cfg, err := llmproxy.LoadConfigWith(configPath, llmproxy.Overrides{
		Host:     host,
		Port:     port,
		LogLevel: logLevel,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm-proxy: %v\n", err)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	router, err := llmproxy.NewRouter(cfg, llmproxy.WithLogger(logger))
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm-proxy: %v\n", err)
		return 2
	}
	defer router.Close()

	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	server := llmproxy.NewServer(cfg, router, logger)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	logger.Info("starting llm-proxy",
		"version", llmproxy.Version,
		"address", address,
		"providers", len(cfg.Providers),
		"models", len(cfg.Models),
		"users", len(cfg.UsersByKey))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, httpServer, logger); err != nil {
		return 1
	}
	return 0
}

// serve runs the HTTP server until it fails or the context is cancelled, then
// shuts it down gracefully. Keeping it separate from flag/config handling makes
// the lifecycle testable in isolation.
func serve(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	failures := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		failures <- err
	}()

	select {
	case err := <-failures:
		if err != nil {
			logger.Error("server stopped", "error", err)
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			return err
		}
		return nil
	}
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warning", "warn":
		return slog.LevelWarn
	case "error", "critical":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
