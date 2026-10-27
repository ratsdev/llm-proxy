package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"missing"}}`))
	})
	other := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	p1 := testProvider("p1", server.URL, "/chat/completions")
	p2 := testProvider("p2", other.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if result.ProviderID != "p1" || result.Status != http.StatusNotFound {
		t.Fatalf("result = %+v", result)
	}
	data, _ := result.ReadAll()
	if !strings.Contains(string(data), "missing") {
		t.Fatalf("error body = %q", data)
	}
}

func TestRouterRetriesAndRecordsRetryAfter(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	p1 := testProvider("p1", server.URL, "/chat/completions")
	p1.MaxRetries = 1
	p2 := testProvider("p2", live.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	var sleeps []time.Duration
	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model),
		WithRand(func() float64 { return 0 }),
		WithSleep(func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}))
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
	if len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

func TestRouterReturnsLastRetryableError(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	})
	p := testProvider("p", server.URL, "/chat/completions")
	p.MaxRetries = 1
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u"}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.OK() || result.Status != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestRouterContextCancellationStopsRetries(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	p := testProvider("p", deadURL, "/chat/completions")
	p.MaxRetries = 3
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u"}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(sleepContext))
	defer router.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := send(ctx, router, model, textRequest("hi"), nil, "", "chat_completions")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryDelayClampsHugeAttempt(t *testing.T) {
	t.Parallel()

	router := &Router{rand: func() float64 { return 1 }}
	if got := router.retryDelay(1000, nil, defaultMaxRetryAfter); got != maxBackoff+maxBackoff/2 {
		t.Fatalf("retryDelay = %v, want %v", got, maxBackoff+maxBackoff/2)
	}
}

func TestRetryDelayAddsJitter(t *testing.T) {
	t.Parallel()

	low := &Router{rand: func() float64 { return 0 }}
	if got := low.retryDelay(0, nil, defaultMaxRetryAfter); got != 250*time.Millisecond {
		t.Fatalf("low jitter delay = %v, want 250ms", got)
	}
	high := &Router{rand: func() float64 { return 1 }}
	if got := high.retryDelay(0, nil, defaultMaxRetryAfter); got != 375*time.Millisecond {
		t.Fatalf("high jitter delay = %v, want 375ms", got)
	}
}

func TestRetryDelayClampsRetryAfterToBindingCap(t *testing.T) {
	t.Parallel()

	router := &Router{rand: func() float64 { return 0 }}
	header := http.Header{"Retry-After": []string{"30"}}
	if got := router.retryDelay(0, header, 2*time.Second); got != 2*time.Second {
		t.Fatalf("retryDelay = %v, want 2s", got)
	}
}

// A Retry-After far beyond the cooldown cap must not overflow time.Duration into
// a negative delay: that would retry immediately and also skip the 429 cooldown.
func TestParseRetryAfterCapsAbsurdValue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"30000000000", "99999999999999999999"} {
		header := http.Header{"Retry-After": []string{value}}
		delay, ok := parseRetryAfter(header, now)
		if !ok || delay != maxCooldown {
			t.Fatalf("Retry-After %q = %v, ok = %v, want %v", value, delay, ok, maxCooldown)
		}
	}

	router := &Router{rand: func() float64 { return 0 }}
	header := http.Header{"Retry-After": []string{"30000000000"}}
	if delay := router.retryDelay(0, header, time.Second); delay != time.Second {
		t.Fatalf("retryDelay = %v, want the 1s binding cap", delay)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	header := http.Header{"Retry-After": []string{now.Add(3 * time.Second).Format(http.TimeFormat)}}
	delay, ok := parseRetryAfter(header, now)
	if !ok || delay != 3*time.Second {
		t.Fatalf("date delay = %v, ok = %v, want 3s", delay, ok)
	}

	past := http.Header{"Retry-After": []string{now.Add(-time.Second).Format(http.TimeFormat)}}
	if delay, ok := parseRetryAfter(past, now); !ok || delay != 0 {
		t.Fatalf("past date delay = %v, ok = %v, want 0", delay, ok)
	}

	invalid := http.Header{"Retry-After": []string{"soon"}}
	if _, ok := parseRetryAfter(invalid, now); ok {
		t.Fatal("invalid Retry-After should not parse")
	}
}
