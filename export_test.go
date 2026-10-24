package llmproxy

import (
	"context"
	"time"
)

// Test-only aliases for the router options that are deliberately unexported in
// production code. They live in a _test file so they never reach the public
// API while in-package tests can still inject deterministic behaviour.
func WithRand(source func() float64) Option { return withRand(source) }

func WithSleep(sleep func(context.Context, time.Duration) error) Option { return withSleep(sleep) }
