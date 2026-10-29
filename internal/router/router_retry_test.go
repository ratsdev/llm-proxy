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

func TestRetryDelay(t *testing.T) {
	t.Parallel()

	retryAfter := func(value string) http.Header {
		return http.Header{"Retry-After": []string{value}}
	}
	cases := []struct {
		name    string
		attempt int
		header  http.Header
		cap     time.Duration
		rand    float64
		want    time.Duration
	}{
		// A huge attempt count must clamp instead of overflowing the backoff.
		{"huge attempt", 1000, nil, defaultMaxRetryAfter, 1, maxBackoff + maxBackoff/2},
		{"jitter low", 0, nil, defaultMaxRetryAfter, 0, 250 * time.Millisecond},
		{"jitter high", 0, nil, defaultMaxRetryAfter, 1, 375 * time.Millisecond},
		{"retry-after capped by binding", 0, retryAfter("30"), 2 * time.Second, 0, 2 * time.Second},
		{"absurd retry-after capped by binding", 0, retryAfter("30000000000"), time.Second, 0, time.Second},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := &Router{rand: func() float64 { return test.rand }}
			if got := router.retryDelay(test.attempt, test.header, test.cap); got != test.want {
				t.Fatalf("retryDelay = %v, want %v", got, test.want)
			}
		})
	}
}

// A Retry-After far beyond the cooldown cap must not overflow time.Duration into
// a negative delay: that would retry immediately and also skip the 429 cooldown.
func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"absurd seconds", "30000000000", maxCooldown, true},
		{"overflowing seconds", "99999999999999999999", maxCooldown, true},
		{"http date", now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, true},
		{"past date", now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{"invalid", "soon", 0, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			delay, ok := parseRetryAfter(http.Header{"Retry-After": []string{test.value}}, now)
			if ok != test.ok || delay != test.want {
				t.Fatalf("parseRetryAfter(%q) = %v, ok = %v; want %v, ok = %v",
					test.value, delay, ok, test.want, test.ok)
			}
		})
	}
}
