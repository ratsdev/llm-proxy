package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestRunVersionExitsZero(t *testing.T) {
	if code := run([]string{"--version"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestRunRequiresConfig(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	if code := run([]string{"--does-not-exist"}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunMissingConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	if code := run([]string{"-c", path}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]string{
		"debug":    "DEBUG",
		"warning":  "WARN",
		"WARN":     "WARN",
		"error":    "ERROR",
		"critical": "ERROR",
		"info":     "INFO",
		"unknown":  "INFO",
	}
	for input, want := range cases {
		if got := parseLevel(input).String(); got != want {
			t.Errorf("parseLevel(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestServeShutsDownOnCancel(t *testing.T) {
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, logger) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancellation")
	}
}
