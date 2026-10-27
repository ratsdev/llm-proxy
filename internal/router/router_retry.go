package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// retryableStatus lists the upstream statuses that trigger another attempt.
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
	headers := upstreamHeaders(provider, target.wireFormat, req.Stream, req.InboundHeaders)
	readTimeout := readTimeoutFor(req.Model, provider)
	maxAfter := maxRetryAfterFor(target.modelProvider)
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
				if err := r.sleep(ctx, r.retryDelay(attempt, nil, maxAfter)); err != nil {
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
			if err := r.sleep(ctx, r.retryDelay(attempt, response.Header, maxAfter)); err != nil {
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

// sendOnce performs one upstream POST. The read timeout is a hard deadline for
// receiving headers and an inactivity deadline while the body is consumed.
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

// retryDelay computes the wait before the next attempt to the same provider.
// Retry-After takes precedence and is clamped to the binding's max_retry_after.
// Otherwise the delay is the usual exponential backoff, capped at 2 s, plus up
// to 50% jitter so clients that failed together do not retry together.
func (r *Router) retryDelay(attempt int, header http.Header, maxAfter time.Duration) time.Duration {
	now := time.Now()
	if delay, ok := parseRetryAfter(header, now); ok {
		if delay > maxAfter {
			return maxAfter
		}
		return delay
	}
	if attempt > maxRetryShift {
		attempt = maxRetryShift
	}
	base := 250 * time.Millisecond << attempt
	if base > maxBackoff {
		base = maxBackoff
	}
	return base + time.Duration(float64(base/2)*r.rand())
}

// parseRetryAfter reads a Retry-After header in either delta-seconds or
// HTTP-date form. A date in the past parses as a zero delay. Delta-seconds are
// capped before the conversion so an absurd value cannot overflow
// time.Duration into a negative delay (which would mean "retry immediately"
// and would also disable the 429 cooldown).
func parseRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if isDigits(value) {
		seconds, err := strconv.Atoi(value)
		if err != nil {
			// Out of int range: treat it as "as long as allowed".
			return maxCooldown, true
		}
		if seconds < 0 {
			return 0, true
		}
		if seconds > int(maxCooldown/time.Second) {
			return maxCooldown, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if delay := when.Sub(now); delay > 0 {
		return delay, true
	}
	return 0, true
}

func isDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
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
