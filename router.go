package llmproxy

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

const (
	stickyCacheSize         = 10_000
	defaultAnthropicVersion = "2023-06-01"
	maxRetryAfter           = 5 * time.Second
	maxBackoff              = 2 * time.Second
	maxErrorBodyBytes       = 1 << 20
	defaultReadTimeout      = 600 * time.Second
	// maxRetryShift keeps the exponential backoff shift well inside int64.
	maxRetryShift = 10
)

var retryableStatus = map[int]bool{
	408: true,
	409: true,
	425: true,
	429: true,
	500: true,
	502: true,
	503: true,
	504: true,
	// Anthropic reports provider-side capacity exhaustion as 529.
	529: true,
}

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
	endpoint      string
}

type stickyEntry struct {
	providerID    string
	upstreamModel string
}

// RouteRequest is one dispatch. Payload is the inbound JSON. ChatFamily marks
// routes that may be translated when the upstream wire differs; same-wire
// candidates forward Payload after sanitizing the model name. Request is an
// optional pre-parsed IR (tests and lazy conversion).
type RouteRequest struct {
	Model           *Model
	Operation       string
	Stream          bool
	ChatFamily      bool
	InboundHeaders  map[string]string
	StickyID        string
	PreferredFormat WireFormat
	Request         *ir.Request
	Payload         map[string]any
}

// Router selects an upstream, applies retries and failover, and returns the
// upstream reply without interpreting its wire format.
type Router struct {
	cfg     *Config
	clients map[string]*http.Client

	logger *slog.Logger
	rand   func() float64
	sleep  func(context.Context, time.Duration) error

	mu       sync.Mutex
	sticky   map[string]stickyEntry
	order    *list.List
	elements map[string]*list.Element
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

// withRand overrides the weighted-selection random source.
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
		if adapterFor(spec.wire) == nil {
			return nil, fmt.Errorf("no adapter registered for wire format %q", spec.wire)
		}
	}

	r := &Router{
		cfg:      cfg,
		clients:  make(map[string]*http.Client),
		logger:   slog.Default(),
		rand:     rand.Float64,
		sleep:    sleepContext,
		sticky:   make(map[string]stickyEntry),
		order:    list.New(),
		elements: make(map[string]*list.Element),
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
		client, err := newHTTPClient(proxyURL)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.clients[proxyURL] = client
	}
	return r, nil
}

// Close releases idle upstream connections.
func (r *Router) Close() {
	for _, client := range r.clients {
		client.CloseIdleConnections()
	}
}

// Send formats the request for each candidate and dispatches it.
func (r *Router) Send(ctx context.Context, req RouteRequest) (*RouteResult, error) {
	parsed := req.Request
	ensure := func() (*ir.Request, error) {
		if parsed != nil {
			return parsed, nil
		}
		if req.Payload == nil {
			return nil, fmt.Errorf("missing request body")
		}
		adapter := adapterFor(req.PreferredFormat)
		if adapter == nil {
			return nil, fmt.Errorf("no adapter for wire format %q", req.PreferredFormat)
		}
		next, err := adapter.ParseRequest(req.Payload)
		if err != nil {
			return nil, util.New(400, "Invalid request body: "+err.Error(), "", "invalid_body", nil)
		}
		parsed = next
		return parsed, nil
	}

	video, err := r.detectVideo(req, ensure)
	if err != nil {
		return nil, err
	}

	prepare := func(target candidate) (map[string]any, error) {
		if req.Payload != nil && (!req.ChatFamily || target.wireFormat == req.PreferredFormat) {
			body := sanitizePayload(req.Payload, r.cfg.DropParams)
			body["model"] = target.modelProvider.UpstreamModel
			return body, nil
		}
		request, err := ensure()
		if err != nil {
			return nil, err
		}
		adapter := adapterFor(target.wireFormat)
		return adapter.FormatRequest(request, target.modelProvider.UpstreamModel, req.Model.MaxOutputLength)
	}
	return r.dispatch(ctx, req, prepare, video)
}

func (r *Router) detectVideo(req RouteRequest, ensure func() (*ir.Request, error)) (bool, error) {
	if req.Request != nil {
		return ir.HasVideo(req.Request), nil
	}
	if !req.ChatFamily || req.Payload == nil || !r.hasNonOpenAI(req.Model) {
		return false, nil
	}
	request, err := ensure()
	if err != nil {
		return false, err
	}
	return ir.HasVideo(request), nil
}

func (r *Router) hasNonOpenAI(model *Model) bool {
	if model == nil {
		return false
	}
	for _, modelProvider := range model.Providers {
		provider, ok := r.cfg.Providers[modelProvider.ProviderID]
		if ok && provider.Protocol != ProtocolOpenAI {
			return true
		}
	}
	return false
}

func (r *Router) candidates(model *Model, operation string, preferred WireFormat, video bool) ([]candidate, error) {
	candidates := make([]candidate, 0, len(model.Providers))
	for _, modelProvider := range model.Providers {
		provider, ok := r.cfg.Providers[modelProvider.ProviderID]
		if !ok {
			return nil, util.New(500, fmt.Sprintf("Provider %q is not configured.", modelProvider.ProviderID),
				"api_error", "internal_error", nil)
		}
		endpoint, wireFormat, ok := provider.OutboundFor(operation, preferred)
		if !ok {
			continue
		}
		if video && provider.Protocol != ProtocolOpenAI {
			// The Messages API has no video content block, so those providers
			// cannot serve this request.
			continue
		}
		candidates = append(candidates, candidate{
			provider:      provider,
			modelProvider: modelProvider,
			wireFormat:    wireFormat,
			endpoint:      endpoint,
		})
	}
	if len(candidates) == 0 {
		if video {
			return nil, util.New(400,
				fmt.Sprintf("Model %q providers do not support video input.", model.ID),
				"", "unsupported_media", nil)
		}
		return nil, util.New(400,
			fmt.Sprintf("Model %q has no provider supporting operation %q", model.ID, operation),
			"", "endpoint_not_supported", nil)
	}
	return candidates, nil
}

func (r *Router) dispatch(
	ctx context.Context,
	req RouteRequest,
	prepare func(candidate) (map[string]any, error),
	video bool,
) (*RouteResult, error) {
	candidates, err := r.candidates(req.Model, req.Operation, req.PreferredFormat, video)
	if err != nil {
		return nil, err
	}

	ordered := r.routeOrder(candidates, req.StickyID)
	routed := false
	defer func() {
		if !routed {
			r.unpin(req.StickyID)
		}
	}()

	failures := make([]string, 0, len(ordered))
	var lastRetryable *RouteResult
	for _, target := range ordered {
		body, err := prepare(target)
		if err != nil {
			// Parse/validation failures are client errors; do not mask them as
			// an internal gateway failure.
			var gatewayError *util.Error
			if errors.As(err, &gatewayError) {
				return nil, gatewayError
			}
			r.logger.Error("failed to format upstream request",
				"provider", target.provider.ID, "operation", req.Operation, "error", err)
			return nil, util.New(500, "Failed to format upstream request.",
				"api_error", "internal_error", nil)
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
			r.pin(req.StickyID, target)
			routed = true
			return result, nil
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

// attempt sends the request to one candidate, retrying transient failures.
// It returns the last retryable reply when the retries are exhausted.
func (r *Router) attempt(
	ctx context.Context,
	target candidate,
	req RouteRequest,
	body map[string]any,
) (*RouteResult, error) {
	payload, err := util.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request body: %w", target.provider.ID, err)
	}

	provider := target.provider
	headers := upstreamHeaders(provider, req.Stream, req.InboundHeaders)
	readTimeout := readTimeoutFor(req.Model, provider)
	attempts := provider.MaxRetries + 1
	client := r.clients[provider.Proxy]
	url := provider.URLFor(target.endpoint)

	var lastRetryable *RouteResult
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		response, err := r.sendOnce(ctx, client, url, headers, payload, readTimeout)
		if err != nil {
			lastErr = err
			r.logger.Warn("upstream request failed",
				"provider", provider.ID, "attempt", attempt+1, "attempts", attempts, "error", err)
			if attempt+1 < attempts {
				if err := r.sleep(ctx, retryDelay(attempt, nil)); err != nil {
					return nil, err
				}
			}
			continue
		}

		result := &RouteResult{
			ProviderID:    provider.ID,
			UpstreamModel: target.modelProvider.UpstreamModel,
			WireFormat:    target.wireFormat,
			Status:        response.StatusCode,
			Header:        response.Header,
			Body:          response.Body,
		}
		if result.OK() {
			return result, nil
		}

		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		_ = response.Body.Close()
		result.Body = io.NopCloser(bytes.NewReader(errorBody))

		if !retryableStatus[result.Status] {
			return result, nil
		}
		lastRetryable = result
		if attempt+1 < attempts {
			r.logger.Warn("upstream error, retrying",
				"provider", provider.ID, "status", result.Status,
				"attempt", attempt+1, "attempts", attempts)
			if err := r.sleep(ctx, retryDelay(attempt, response.Header)); err != nil {
				return nil, err
			}
			continue
		}
	}

	if lastRetryable != nil {
		return lastRetryable, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no attempt was made")
	}
	return nil, fmt.Errorf("%s: %w", provider.ID, lastErr)
}

func (r *Router) sendOnce(
	ctx context.Context,
	client *http.Client,
	target string,
	headers http.Header,
	payload []byte,
	readTimeout time.Duration,
) (*http.Response, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(readTimeout, cancel)

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		timer.Stop()
		cancel()
		return nil, err
	}
	request.Header = headers.Clone()

	response, err := client.Do(request)
	timer.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	if ctxErr := requestCtx.Err(); ctxErr != nil {
		_ = response.Body.Close()
		cancel()
		return nil, ctxErr
	}
	response.Body = newIdleTimeoutBody(response.Body, readTimeout, cancel)
	return response, nil
}

func (r *Router) routeOrder(candidates []candidate, requestID string) []candidate {
	r.mu.Lock()
	defer r.mu.Unlock()

	if requestID != "" {
		if entry, ok := r.sticky[requestID]; ok {
			for index, item := range candidates {
				if item.modelProvider.ProviderID == entry.providerID &&
					item.modelProvider.UpstreamModel == entry.upstreamModel {
					r.touchLocked(requestID)
					ordered := make([]candidate, 0, len(candidates))
					ordered = append(ordered, item)
					ordered = append(ordered, candidates[:index]...)
					ordered = append(ordered, candidates[index+1:]...)
					return ordered
				}
			}
			// The pinned upstream cannot serve this request any more.
			r.removeLocked(requestID)
		}
	}

	ordered := r.weightedOrderLocked(candidates)
	// Sticky state is written only once a provider has actually served the
	// request, so a tentative pick that is about to fail cannot be observed by
	// a concurrent request carrying the same id.
	return ordered
}

func (r *Router) weightedOrderLocked(candidates []candidate) []candidate {
	remaining := append([]candidate(nil), candidates...)
	ordered := make([]candidate, 0, len(candidates))
	for len(remaining) > 0 {
		total := 0.0
		for _, candidate := range remaining {
			total += candidate.modelProvider.Weight
		}
		pick := r.rand() * total
		consumed := 0.0
		selected := len(remaining) - 1
		for index, candidate := range remaining {
			consumed += candidate.modelProvider.Weight
			if pick < consumed {
				selected = index
				break
			}
		}
		ordered = append(ordered, remaining[selected])
		remaining = append(remaining[:selected], remaining[selected+1:]...)
	}
	return ordered
}

func (r *Router) pin(requestID string, entry candidate) {
	if requestID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinLocked(requestID, entry)
}

func (r *Router) pinLocked(requestID string, entry candidate) {
	if requestID == "" {
		return
	}
	if element, ok := r.elements[requestID]; ok {
		r.order.MoveToBack(element)
	} else {
		r.elements[requestID] = r.order.PushBack(requestID)
	}
	r.sticky[requestID] = stickyEntry{
		providerID:    entry.modelProvider.ProviderID,
		upstreamModel: entry.modelProvider.UpstreamModel,
	}
	for r.order.Len() > stickyCacheSize {
		front := r.order.Front()
		if front == nil {
			break
		}
		r.order.Remove(front)
		evicted, _ := front.Value.(string)
		delete(r.elements, evicted)
		delete(r.sticky, evicted)
	}
}

func (r *Router) touchLocked(requestID string) {
	if element, ok := r.elements[requestID]; ok {
		r.order.MoveToBack(element)
	}
}

func (r *Router) removeLocked(requestID string) {
	if element, ok := r.elements[requestID]; ok {
		r.order.Remove(element)
		delete(r.elements, requestID)
	}
	delete(r.sticky, requestID)
}

func (r *Router) unpin(requestID string) {
	if requestID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(requestID)
}

func upstreamHeaders(provider *Provider, stream bool, inbound map[string]string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	headers.Set("User-Agent", "llm-proxy/"+Version)

	if provider.Key != "" {
		if provider.Protocol == ProtocolAnthropic {
			headers.Set("x-api-key", provider.Key)
		} else {
			headers.Set("Authorization", "Bearer "+provider.Key)
		}
	}
	if provider.Protocol == ProtocolAnthropic {
		version := inbound["anthropic-version"]
		if version == "" {
			version = defaultAnthropicVersion
		}
		headers.Set("anthropic-version", version)
		if beta := inbound["anthropic-beta"]; beta != "" {
			headers.Set("anthropic-beta", beta)
		}
	}
	// Configured headers are applied last so operators can override anything
	// above, including the Authorization/Accept defaults set here.
	for key, value := range provider.Headers {
		headers.Set(key, value)
	}
	return headers
}

func readTimeoutFor(model *Model, provider *Provider) time.Duration {
	switch {
	case model.Timeout != nil && *model.Timeout > 0:
		return time.Duration(*model.Timeout * float64(time.Second))
	case provider.Timeout != nil && *provider.Timeout > 0:
		return time.Duration(*provider.Timeout * float64(time.Second))
	default:
		return defaultReadTimeout
	}
}

func retryDelay(attempt int, header http.Header) time.Duration {
	if header != nil {
		retryAfter := strings.TrimSpace(header.Get("Retry-After"))
		if retryAfter != "" && isDigits(retryAfter) {
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				delay := time.Duration(seconds) * time.Second
				if delay > maxRetryAfter {
					return maxRetryAfter
				}
				return delay
			}
		}
	}
	if attempt > maxRetryShift {
		attempt = maxRetryShift
	}
	delay := 250 * time.Millisecond << attempt
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

func isDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
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

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
