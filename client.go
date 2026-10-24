package llmproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	dialTimeout         = 10 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConns        = 50
	maxConnsPerHost     = 200
)

// newHTTPClient builds an upstream client. A non-empty proxy URL selects an
// HTTP(S) or SOCKS5 proxy; environment proxy variables are never consulted.
func newHTTPClient(proxyURL string) (*http.Client, error) {
	direct := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           direct.DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConns,
		MaxConnsPerHost:       maxConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", proxyURL, err)
		}
		switch parsed.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if parsed.User != nil {
				password, _ := parsed.User.Password()
				auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
			}
			dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, direct)
			if err != nil {
				return nil, fmt.Errorf("build socks5 dialer %q: %w", proxyURL, err)
			}
			if contextDialer, ok := dialer.(interface {
				DialContext(ctx context.Context, network, address string) (net.Conn, error)
			}); ok {
				transport.DialContext = contextDialer.DialContext
			} else {
				transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
					return dialer.Dial(network, address)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
		}
	}

	return &http.Client{
		Transport: transport,
		// The gateway must surface 3xx responses instead of following them;
		// wire-format semantics treat them as successful upstream replies.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// idleTimeoutBody enforces an inactivity timeout while reading an upstream
// response body. The timer is reset on every successful read, so a slow but
// steady stream is not interrupted.
type idleTimeoutBody struct {
	body    io.ReadCloser
	timer   *time.Timer
	cancel  context.CancelFunc
	timeout time.Duration
	once    sync.Once
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *idleTimeoutBody {
	wrapped := &idleTimeoutBody{body: body, cancel: cancel, timeout: timeout}
	wrapped.timer = time.AfterFunc(timeout, cancel)
	return wrapped
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.timeout)
	n, err := b.body.Read(p)
	if err == nil {
		b.timer.Reset(b.timeout)
	} else {
		b.timer.Stop()
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	var err error
	b.once.Do(func() {
		b.timer.Stop()
		b.cancel()
		err = b.body.Close()
	})
	return err
}
