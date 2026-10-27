package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const (
	stickyCacheSize         = 10_000
	defaultAnthropicVersion = "2023-06-01"
	// defaultMaxRetryAfter bounds how long one provider binding waits for a
	// Retry-After before retrying. models.*.providers[].max_retry_after
	// overrides it per binding.
	defaultMaxRetryAfter = 5 * time.Second
	maxBackoff           = 2 * time.Second
	maxErrorBodyBytes    = 1 << 20
	// DefaultReadTimeout bounds one upstream reply (headers plus body) when the
	// model and provider configure no timeout of their own.
	DefaultReadTimeout = 600 * time.Second
	// A provider that reports 429 is deprioritised for one model for the
	// Retry-After interval, or for defaultCooldown when the header is absent.
	defaultCooldown = 30 * time.Second
	maxCooldown     = 5 * time.Minute
	// maxRetryShift keeps the exponential backoff shift well inside int64.
	maxRetryShift = 10
)

// RouteResult is a selected upstream reply. Callers must Close it exactly once.
type RouteResult struct {
	ProviderID    string
	UpstreamModel string
	WireFormat    WireFormat
	Status        int
	Header        http.Header
	Body          io.ReadCloser
}

// OK reports whether the upstream accepted the request.
func (r *RouteResult) OK() bool {
	return r.Status < 400
}

// ReadAll reads the whole response body.
func (r *RouteResult) ReadAll() ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.Body)
}

// Close releases the upstream connection. It is safe to call more than once.
func (r *RouteResult) Close() error {
	if r.Body == nil {
		return nil
	}
	err := r.Body.Close()
	r.Body = nil
	return err
}

// Candidate is one upstream that can serve a request for a model.
type candidate struct {
	provider      *Provider
	modelProvider ModelProvider
	wireFormat    WireFormat
	endpoint      ProviderEndpoint
}

// RouteRequest is one dispatch. Payload is the inbound JSON body in the
// PreferredFormat wire format.
type RouteRequest struct {
	Model           *Model
	Stream          bool
	InboundHeaders  map[string]string
	StickyID        string
	PreferredFormat WireFormat
	Payload         map[string]any
}

// Router selects an upstream, applies retries and failover, and returns the
// upstream reply without interpreting its wire format.
type Router struct {
	cfg     *Config
	clients map[string]*http.Client
	pool    *TransportPool
	// ownsPool reports that the router built its own transport pool and must
	// close it; a router built on a shared pool leaves it to the owner.
	ownsPool bool

	logger *slog.Logger
	rand   func() float64
	sleep  func(context.Context, time.Duration) error

	// state holds the caches that survive a configuration reload.
	state *State
}

// Option customises a Router.
type Option func(*Router)

// WithLogger overrides the logger used for routing diagnostics.
func WithLogger(logger *slog.Logger) Option {
	return func(r *Router) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// WithState continues the routing memory of a previous router. Reloading the
// configuration must reuse one State so conversation pins, 429 cooldowns and
// round-robin cursors survive an administrative write.
func WithState(state *State) Option {
	return func(r *Router) {
		if state != nil {
			r.state = state
		}
	}
}

// WithTransportPool builds the router on a caller-owned pool of upstream HTTP
// clients. The pool keeps connections warm across reloads; the caller closes
// it when the gateway shuts down.
func WithTransportPool(pool *TransportPool) Option {
	return func(r *Router) {
		if pool != nil {
			r.pool = pool
			r.ownsPool = false
		}
	}
}

// withRand overrides the random source used by the random routing strategy.
func withRand(source func() float64) Option {
	return func(r *Router) { r.rand = source }
}

// withSleep overrides retry backoff waiting.
func withSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(r *Router) { r.sleep = sleep }
}

// NewRouter builds a router and one HTTP client per distinct proxy URL.
func NewRouter(cfg *Config, opts ...Option) (*Router, error) {
	for _, spec := range routeSpecs {
		if convert.AdapterFor(spec.Wire) == nil {
			return nil, fmt.Errorf("no protocol adapter registered for wire format %q", spec.Wire)
		}
	}

	r := &Router{
		cfg:      cfg,
		clients:  make(map[string]*http.Client),
		pool:     NewTransportPool(),
		ownsPool: true,
		logger:   slog.Default(),
		rand:     rand.Float64,
		sleep:    sleepContext,
		state:    NewState(),
	}
	for _, opt := range opts {
		opt(r)
	}

	proxies := []string{""}
	for _, provider := range cfg.Providers {
		if provider.Proxy != "" && !slices.Contains(proxies, provider.Proxy) {
			proxies = append(proxies, provider.Proxy)
		}
	}
	for _, proxyURL := range proxies {
		client, err := r.pool.Client(proxyURL)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.clients[proxyURL] = client
	}
	return r, nil
}

// Close releases idle upstream connections. A router that did not build its own
// transport pool leaves the shared connections alone; the pool owner closes
// them at shutdown.
func (r *Router) Close() {
	if !r.ownsPool {
		return
	}
	r.pool.Close()
}

// Send converts the request for each candidate and dispatches it. Candidates are
// only built from providers whose endpoint declares a converter for the inbound
// wire format, so a body is either forwarded unchanged (apart from the model
// name) or losslessly converted.
func (r *Router) Send(ctx context.Context, req RouteRequest) (*RouteResult, error) {
	if req.Payload == nil {
		return nil, fmt.Errorf("missing request body")
	}
	prepare := func(target candidate) (map[string]any, error) {
		if target.wireFormat == req.PreferredFormat {
			body := sanitizePayload(req.Payload, r.cfg.DropParams)
			body["model"] = target.modelProvider.UpstreamModel
			return body, nil
		}
		body, err := convert.Request(
			req.PreferredFormat,
			target.wireFormat,
			sanitizePayload(req.Payload, r.cfg.DropParams),
			target.modelProvider.UpstreamModel,
		)
		if err != nil {
			return nil, util.New(400,
				fmt.Sprintf("No lossless converter from %s to %s.", req.PreferredFormat, target.wireFormat),
				"invalid_request_error", "unsupported_conversion", nil)
		}
		return body, nil
	}
	return r.dispatch(ctx, req, prepare)
}

// candidateEndpoint picks the endpoint configured for the inbound wire format.
// The endpoint's converter declares the upstream format; "none" keeps the
// inbound format and passes the body through unchanged.
func candidateEndpoint(provider *Provider, preferred WireFormat) (ProviderEndpoint, WireFormat, bool) {
	endpoint, ok := provider.Endpoints[preferred]
	if !ok {
		return ProviderEndpoint{}, "", false
	}
	wire, ok := endpoint.UpstreamFormat(preferred)
	if !ok {
		return ProviderEndpoint{}, "", false
	}
	return endpoint, wire, true
}

func (r *Router) candidates(model *Model, preferred WireFormat) ([]candidate, error) {
	candidates := make([]candidate, 0, len(model.Providers))
	for _, modelProvider := range model.Providers {
		provider, ok := r.cfg.Providers[modelProvider.ProviderID]
		if !ok {
			return nil, util.New(500, fmt.Sprintf("Provider %q is not configured.", modelProvider.ProviderID),
				"api_error", "internal_error", nil)
		}
		endpoint, wire, ok := candidateEndpoint(provider, preferred)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{
			provider:      provider,
			modelProvider: modelProvider,
			wireFormat:    wire,
			endpoint:      endpoint,
		})
	}
	if len(candidates) == 0 {
		return nil, util.New(400,
			fmt.Sprintf("Model %q cannot serve %s: no provider configures that endpoint with a supported converter.",
				model.ID, preferred),
			"invalid_request_error", "unsupported_conversion", nil)
	}
	return candidates, nil
}

func (r *Router) dispatch(
	ctx context.Context,
	req RouteRequest,
	prepare func(candidate) (map[string]any, error),
) (*RouteResult, error) {
	candidates, err := r.candidates(req.Model, req.PreferredFormat)
	if err != nil {
		return nil, err
	}

	ordered := r.state.routeOrder(candidates, req.StickyID, req.Model.ID, req.Model.Routing, r.rand)
	routed := false
	defer func() {
		if !routed {
			r.state.unpin(req.StickyID)
		}
	}()

	failures := make([]string, 0, len(ordered))
	var lastRetryable *RouteResult
	for _, target := range ordered {
		body, err := prepare(target)
		if err != nil {
			// A conversion failure is already a client-facing error.
			return nil, err
		}

		result, err := r.attempt(ctx, target, req, body)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			failures = append(failures, err.Error())
			continue
		}
		if result.OK() {
			r.state.pin(req.StickyID, target)
			routed = true
			return result, nil
		}
		if result.Status == http.StatusTooManyRequests {
			r.state.startCooldown(req.Model.ID, target.provider.ID, result.Header)
		}
		if retryableStatus[result.Status] {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", target.provider.ID, result.Status))
			lastRetryable = result
			continue
		}
		return result, nil
	}

	if lastRetryable != nil {
		return lastRetryable, nil
	}
	message := "All providers failed"
	if len(failures) > 0 {
		message += ": " + strings.Join(failures, "; ")
	}
	return nil, util.New(502, message, "upstream_error", "upstream_error", nil)
}

func sanitizePayload(payload map[string]any, dropParams bool) map[string]any {
	body := make(map[string]any, len(payload))
	for key, value := range payload {
		body[key] = value
	}
	if !dropParams {
		return body
	}
	for key, value := range body {
		if value == nil || strings.HasPrefix(key, "litellm_") || strings.HasPrefix(key, "_") {
			delete(body, key)
		}
	}
	return body
}
