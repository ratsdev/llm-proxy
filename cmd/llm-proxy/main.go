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

const (
	defaultListenPort = 4000
	// readTimeout bounds reading one request, including its body. Streaming
	// replies are unaffected: the deadline covers the inbound request only, and
	// it keeps a client from trickling a 64 MiB body indefinitely.
	readTimeout = 2 * time.Minute
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("llm-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	var (
		dbPath      string
		host        string
		port        int
		logLevel    string
		showVersion bool
	)
	flags.StringVar(&dbPath, "db", envOr("LLM_PROXY_DB", llmproxy.DefaultDatabasePath),
		"path to the SQLite database (created on first start)")
	flags.StringVar(&host, "host", envOr("LLM_PROXY_HOST", "0.0.0.0"), "listen address")
	flags.IntVar(&port, "port", envIntOr("LLM_PROXY_PORT", defaultListenPort), "listen port")
	flags.StringVar(&logLevel, "log-level", envOr("LLM_PROXY_LOG_LEVEL", "info"),
		"log level: debug, info, warning, error")
	flags.BoolVar(&showVersion, "version", false, "print the version and exit")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if showVersion {
		fmt.Printf("llm-proxy %s\n", llmproxy.Version)
		return 0
	}
	if port < 1 || port > 65535 {
		fmt.Fprintln(os.Stderr, "llm-proxy: --port must be between 1 and 65535")
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(logLevel),
	}))
	slog.SetDefault(logger)

	store, err := llmproxy.OpenStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm-proxy: %v\n", err)
		return 2
	}
	defer func() { _ = store.Close() }()

	initialized, err := store.HasAdmin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm-proxy: %v\n", err)
		return 2
	}
	if !initialized {
		logger.Warn("no administrator account exists",
			"note", "open the Web console to register the first administrator account")
	}

	app, err := llmproxy.NewApp(store, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm-proxy: %v\n", err)
		return 2
	}
	defer app.Close()

	address := net.JoinHostPort(host, strconv.Itoa(port))
	httpServer := &http.Server{
		Addr:              address,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	cfg := app.Config()
	logger.Info("starting llm-proxy",
		"version", llmproxy.Version,
		"address", address,
		"database", store.Path(),
		"providers", len(cfg.Providers),
		"models", len(cfg.Models),
		"api_keys", len(cfg.UsersByKey),
		"ui", "http://"+address+"/")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, httpServer, logger); err != nil {
		return 1
	}
	return 0
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		// Silently falling back would hide a typo in a deployment manifest.
		fmt.Fprintf(os.Stderr, "llm-proxy: ignoring invalid %s=%q, using %d\n", key, raw, fallback)
		return fallback
	}
	return value
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
	case "info", "":
		return slog.LevelInfo
	case "warning", "warn":
		return slog.LevelWarn
	case "error", "critical":
		return slog.LevelError
	default:
		// Falling back silently would hide a typo in a deployment manifest.
		fmt.Fprintf(os.Stderr, "llm-proxy: unknown log level %q, using info\n", value)
		return slog.LevelInfo
	}
}
