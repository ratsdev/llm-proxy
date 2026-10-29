package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	routing "github.com/ratsdev/llm-proxy/internal/router"
)

func TestServerPreservesClientRequestID(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return errorResult(model, http.StatusTooManyRequests, `{"error":{"message":"slow down","type":"rate_limit_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/chat/completions",
		strings.NewReader(`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer key-a")
	request.Header.Set("x-request-id", "client-req_1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("x-request-id"); got != "client-req_1" {
		t.Fatalf("request id = %q", got)
	}
	if got := response.Header.Get("x-provider-id"); got != "provider" {
		t.Fatalf("provider header = %q", got)
	}
	if got := response.Header.Get("x-upstream-model"); got != "model-a" {
		t.Fatalf("upstream header = %q", got)
	}
	errorObject := decodeBody(t, response)["error"].(map[string]any)
	if errorObject["message"] != "slow down" || errorObject["type"] != "rate_limit_error" {
		t.Fatalf("error = %v", errorObject)
	}

	router.mu.Lock()
	stickyID := router.lastStickyID
	router.mu.Unlock()
	if stickyID != routing.SessionDigest("model/a", "alice", "first-user=hi") {
		t.Fatalf("sticky id = %q", stickyID)
	}
}

// Console logins are access-log operations too: an operator has to see who
// opened the console, not only the attempts that failed.
func TestServerLogsSuccessfulConsoleLogin(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	cfg := &Config{
		UsersByID: map[string]*User{
			testAdminUsername: {ID: testAdminUsername, IsAdmin: true, PasswordHash: testAdminPasswordHash()},
		},
		HasAdmin: true,
	}
	server := NewServerWithOptions(cfg, &fakeRouter{}, slog.New(slog.NewTextHandler(logs, nil)), ServerOptions{})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := doJSON(t, http.MethodPost, httpServer.URL+"/api/ui/login", "",
		`{"username":"`+testAdminUsername+`","password":"`+testAdminPassword+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)

	deadline := time.Now().Add(time.Second)
	for {
		output := logs.String()
		if strings.Contains(output, "status=200") && strings.Contains(output, "user="+testAdminUsername) {
			if count := strings.Count(output, "ui=login"); count != 1 {
				t.Fatalf("ui attribute reported %d times: %q", count, output)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("access log = %q", output)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServerRecoversFromPanic(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			panic("boom")
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
	errorObject := decodeBody(t, response)["error"].(map[string]any)
	if errorObject["code"] != "internal_error" {
		t.Fatalf("error = %v", errorObject)
	}
}

func TestServerGeneratesRequestID(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	id := response.Header.Get("x-request-id")
	if !strings.HasPrefix(id, "req_") || len(id) != len("req_")+32 {
		t.Fatalf("generated request id = %q", id)
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/health", nil)
	request.Header.Set("x-request-id", "bad id with spaces")
	response2, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response2.Body.Close()
	if got := response2.Header.Get("x-request-id"); !strings.HasPrefix(got, "req_") {
		t.Fatalf("invalid client id must be replaced: %q", got)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Requests failing before the relay (authentication, body parsing, model
// resolution) must still produce an access-log line with status and duration.
func TestServerLogsFailedRequests(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	cfg := &Config{UsersByKey: map[string]*User{
		"key-a": {ID: "alice"},
	}}
	server := NewServerWithOptions(cfg, &fakeRouter{}, slog.New(slog.NewTextHandler(logs, nil)), ServerOptions{})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := doJSON(t, http.MethodPost, httpServer.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)

	deadline := time.Now().Add(time.Second)
	for {
		output := logs.String()
		if strings.Contains(output, "request completed") && strings.Contains(output, "status=401") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("access log = %q", output)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Method-not-allowed and not-found responses never reach a handler that opts
// into logging, so they must be enabled explicitly.
func TestServerLogsRejectedPaths(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	server := NewServerWithOptions(&Config{}, &fakeRouter{}, slog.New(slog.NewTextHandler(logs, nil)), ServerOptions{})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	cases := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/v1/chat/completions", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/missing", http.StatusNotFound},
	}
	for _, test := range cases {
		response := doJSON(t, test.method, httpServer.URL+test.path, "", "")
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: status = %d, want %d", test.method, test.path, response.StatusCode, test.status)
		}
		_, _ = io.Copy(io.Discard, response.Body)

		deadline := time.Now().Add(time.Second)
		statusNeedle := "status=" + strconv.Itoa(test.status)
		for !strings.Contains(logs.String(), statusNeedle) {
			if time.Now().After(deadline) {
				t.Fatalf("%s %s: access log = %q", test.method, test.path, logs.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// A streamed reply must reach the console with a TTFT measured at the first
// frame and a throughput derived from the time spent after that frame, not
// from the whole round trip.
func TestObserveRecordsStreamTiming(t *testing.T) {
	t.Parallel()

	const firstFrame = "data: {\"id\":\"chatcmpl-1\",\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	const lastFrames = "data: {\"id\":\"chatcmpl-1\",\"model\":\"model-a\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":20}}\n\n" +
		"data: [DONE]\n\n"

	consumer, producer := io.Pipe()
	t.Cleanup(func() { _ = consumer.Close() })
	// The upstream starts emitting only once the gateway reads the body, so the
	// delays measure what the client waits for, not how long the test took to
	// set up.
	produce := func() {
		defer producer.Close()
		time.Sleep(80 * time.Millisecond)
		_, _ = producer.Write([]byte(firstFrame))
		time.Sleep(120 * time.Millisecond)
		_, _ = producer.Write([]byte(lastFrames))
	}

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			go produce()
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:          consumer,
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	_ = readBody(t, response)

	client := uiLogin(t, server)
	record := firstConsoleRecord(t, client, server)

	ttft, _ := record["ttft_ms"].(float64)
	duration, _ := record["duration_ms"].(float64)
	if ttft < 70 || ttft >= duration {
		t.Fatalf("ttft = %v, duration = %v", ttft, duration)
	}

	overview := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/overview", "", nil))
	summary, _ := overview["summary"].(map[string]any)
	avgTTFT, _ := summary["avg_ttft_ms"].(float64)
	if avgTTFT != ttft {
		t.Fatalf("summary avg ttft = %v, record ttft = %v", avgTTFT, ttft)
	}
	if tps, _ := summary["avg_output_tps"].(float64); tps <= 0 {
		t.Fatalf("summary output throughput = %v", tps)
	}
}

// An upstream that ignores stream and answers with a buffered JSON body has no
// first-token timing, so the console must report it as unknown instead of
// reusing the round-trip time.
func TestObserveSkipsTTFTForBufferedReplies(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	_ = readBody(t, response)

	record := firstConsoleRecord(t, uiLogin(t, server), server)
	if ttft, _ := record["ttft_ms"].(float64); ttft != 0 {
		t.Fatalf("buffered reply ttft = %v", ttft)
	}
}

// firstConsoleRecord waits for the dashboard to report one completed request.
func firstConsoleRecord(t *testing.T, client *http.Client, server *httptest.Server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		payload := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/requests", "", nil))
		if list, _ := payload["records"].([]any); len(list) > 0 {
			record, _ := list[0].(map[string]any)
			return record
		}
		if time.Now().After(deadline) {
			t.Fatal("request was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResponseStatusReportsAbandonedRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

	recorder := &responseRecorder{ResponseWriter: httptest.NewRecorder()}
	if status := responseStatus(recorder, request); status != statusClientClosedRequest {
		t.Fatalf("abandoned request status = %d", status)
	}

	recorder.WriteHeader(http.StatusCreated)
	if status := responseStatus(recorder, request); status != http.StatusCreated {
		t.Fatalf("committed response status = %d", status)
	}
}

// timeToFirstByte must stay unset until the handler actually writes a body: a
// stream that only commits headers has not produced its first token yet.
func TestResponseRecorderTracksFirstByte(t *testing.T) {
	t.Parallel()

	started := time.Now().Add(-50 * time.Millisecond)
	recorder := &responseRecorder{ResponseWriter: httptest.NewRecorder()}
	if ttft := recorder.timeToFirstByte(started); ttft != 0 {
		t.Fatalf("ttft before first write = %d", ttft)
	}

	recorder.WriteHeader(http.StatusOK)
	if ttft := recorder.timeToFirstByte(started); ttft != 0 {
		t.Fatalf("ttft after headers only = %d", ttft)
	}

	if _, err := recorder.Write([]byte("data: {}\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := recorder.firstWrite
	if first.IsZero() {
		t.Fatal("first write was not timestamped")
	}
	if ttft := recorder.timeToFirstByte(started); ttft < 45 {
		t.Fatalf("ttft = %d, want at least 45ms", ttft)
	}

	time.Sleep(2 * time.Millisecond)
	if _, err := recorder.Write([]byte("more")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !recorder.firstWrite.Equal(first) {
		t.Fatal("first write timestamp moved on a later write")
	}
}
