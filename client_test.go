package llmproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := newHTTPClient("")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.CloseIdleConnections()

	response, err := client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d; redirects must not be followed", response.StatusCode)
	}
}

func TestIdleTimeoutBody(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()
	defer close(release)

	client, err := newHTTPClient("")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	body := newIdleTimeoutBody(response.Body, 40*time.Millisecond, cancel)
	defer body.Close()

	buffer := make([]byte, 16)
	started := time.Now()
	if _, err := body.Read(buffer); err == nil {
		t.Fatal("expected an inactivity timeout")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestHTTPClientProxyValidation(t *testing.T) {
	t.Parallel()

	if _, err := newHTTPClient("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("unsupported scheme must fail")
	}
	cases := []string{
		"http://127.0.0.1:3128",
		"https://127.0.0.1:3128",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
	}
	for _, proxyURL := range cases {
		client, err := newHTTPClient(proxyURL)
		if err != nil {
			t.Fatalf("%s: %v", proxyURL, err)
		}
		client.CloseIdleConnections()
	}
}
