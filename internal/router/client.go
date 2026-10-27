package router

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
	// maxStreamDuration bounds the total time one upstream reply may spend
	// streaming. It is a backstop against a provider that keeps trickling bytes
	// forever: the idle timeout below only fires when the stream stalls.
	maxStreamDuration = time.Hour
)

// TransportPool keeps upstream HTTP clients alive across router generations.
// Rebuilding a router after a configuration change must not drop a warm
// connection pool, so App owns one pool and every router it builds borrows from
// it.
type TransportPool struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

// NewTransportPool builds an empty pool.
func NewTransportPool() *TransportPool {
	return &TransportPool{clients: make(map[string]*http.Client)}
}

// Client returns the shared client for one proxy URL, building it on first use.
func (p *TransportPool) Client(proxyURL string) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[proxyURL]; ok {
		return client, nil
	}
	client, err := newHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	p.clients[proxyURL] = client
	return client, nil
}

// Prune releases the idle connections of clients whose proxy URL is no longer
// configured, so removing a provider does not keep its sockets around.
func (p *TransportPool) Prune(keep map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for proxyURL, client := range p.clients {
		if keep[proxyURL] {
			continue
		}
		client.CloseIdleConnections()
		delete(p.clients, proxyURL)
	}
}

// Close releases every idle connection in the pool.
func (p *TransportPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, client := range p.clients {
		client.CloseIdleConnections()
	}
}

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

// idleTimeoutBody enforces an inactivity timeout and an overall deadline while
// reading an upstream response body. The inactivity timer is reset on every
// successful read, so a slow but steady stream is not interrupted; the overall
// deadline is never reset.
type idleTimeoutBody struct {
	body    io.ReadCloser
	idle    *time.Timer
	total   *time.Timer
	cancel  context.CancelFunc
	timeout time.Duration
	once    sync.Once
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *idleTimeoutBody {
	wrapped := &idleTimeoutBody{body: body, cancel: cancel, timeout: timeout}
	wrapped.idle = time.AfterFunc(timeout, cancel)
	wrapped.total = time.AfterFunc(maxStreamDuration, cancel)
	return wrapped
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	b.idle.Reset(b.timeout)
	n, err := b.body.Read(p)
	if err == nil {
		b.idle.Reset(b.timeout)
	} else {
		b.idle.Stop()
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	var err error
	b.once.Do(func() {
		b.idle.Stop()
		b.total.Stop()
		b.cancel()
		err = b.body.Close()
	})
	return err
}
