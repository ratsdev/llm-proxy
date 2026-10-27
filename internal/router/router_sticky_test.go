package router

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/internal/core"
)

func TestRouterStickyRouting(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, "/chat/completions")
	p2 := testProvider("p2", server.URL, "/chat/completions")
	model := &Model{ID: "m", Routing: RoutingRandom, Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	var mu sync.Mutex
	randomCalls := 0
	source := func() float64 {
		mu.Lock()
		defer mu.Unlock()
		randomCalls++
		if randomCalls == 1 {
			return 0
		}
		return 0.999
	}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(source), WithSleep(noSleep))
	defer router.Close()

	first, err := send(t.Context(), router, model, textRequest("hi"), nil, "req-1", "chat_completions")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	_ = first.Close()

	mu.Lock()
	callsAfterFirst := randomCalls
	mu.Unlock()

	second, err := send(t.Context(), router, model, textRequest("hi"), nil, "req-1", "chat_completions")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	_ = second.Close()

	if first.ProviderID != second.ProviderID {
		t.Fatalf("sticky providers = %q, %q", first.ProviderID, second.ProviderID)
	}
	mu.Lock()
	afterSecond := randomCalls
	mu.Unlock()
	if afterSecond != callsAfterFirst {
		t.Fatalf("sticky request consulted the random source %d extra times", afterSecond-callsAfterFirst)
	}
}

func TestRouterRepinsAfterFailover(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", deadURL, "/chat/completions")
	p1.MaxRetries = 0
	p2 := testProvider("p2", live.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "req-1", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = result.Close()

	router.state.mu.Lock()
	pinned := router.state.sticky["req-1"]
	router.state.mu.Unlock()
	if pinned.providerID != "p2" {
		t.Fatalf("pinned provider = %q", pinned.providerID)
	}
}

// A router built after a configuration reload continues the routing memory of
// the previous generation: the conversation stays on the provider that served
// it even though priority routing would start with the first candidate.
func TestRouterHonoursStateOfPreviousGeneration(t *testing.T) {
	t.Parallel()

	var p1Hits, p2Hits atomic.Int32
	p1Server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		p1Hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	p2Server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		p2Hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	p1 := testProvider("p1", p1Server.URL, "/chat/completions")
	p2 := testProvider("p2", p2Server.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	state := NewState()
	state.pin("session", candidate{
		modelProvider: ModelProvider{ProviderID: "p2", UpstreamModel: "u2"},
	})

	router, err := NewRouter(testConfig([]*Provider{p1, p2}, model), WithState(state), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "session", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q, want the pinned p2", result.ProviderID)
	}
	if p1Hits.Load() != 0 || p2Hits.Load() != 1 {
		t.Fatalf("upstream hits = p1:%d p2:%d", p1Hits.Load(), p2Hits.Load())
	}
}

func TestRouterStickyCacheIsBounded(t *testing.T) {
	t.Parallel()

	router, err := NewRouter(&Config{
		Providers:  map[string]*Provider{},
		Models:     map[string]*Model{},
		UsersByKey: map[string]*core.User{},
	}, WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	for index := 0; index < stickyCacheSize+5; index++ {
		router.state.pin(fmt.Sprintf("req-%d", index), candidate{
			modelProvider: ModelProvider{ProviderID: "p", UpstreamModel: "u"},
		})
	}
	router.state.mu.Lock()
	size := router.state.order.Len()
	entries := len(router.state.sticky)
	router.state.mu.Unlock()
	if size != stickyCacheSize || entries != stickyCacheSize {
		t.Fatalf("cache size = %d, entries = %d", size, entries)
	}
}

// Cooldowns and rotation cursors follow the configured catalog: state for a
// deleted model must not stay in memory for the life of the process.
func TestStatePrunesRemovedModels(t *testing.T) {
	t.Parallel()

	state := NewState()
	state.cooldown[cooldownKey{modelID: "gone", providerID: "p"}] = time.Now().Add(time.Minute)
	state.cooldown[cooldownKey{modelID: "kept", providerID: "p"}] = time.Now().Add(time.Minute)
	state.roundRobin["gone"] = 3
	state.roundRobin["kept"] = 3

	state.PruneModels(map[string]bool{"kept": true})

	if _, ok := state.cooldown[cooldownKey{modelID: "gone", providerID: "p"}]; ok {
		t.Fatal("cooldown for a deleted model survived")
	}
	if _, ok := state.roundRobin["gone"]; ok {
		t.Fatal("rotation cursor for a deleted model survived")
	}
	if len(state.cooldown) != 1 || len(state.roundRobin) != 1 {
		t.Fatalf("state = %v / %v", state.cooldown, state.roundRobin)
	}
}

func TestRouterConcurrentSticky(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, "/chat/completions")
	p2 := testProvider("p2", server.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithSleep(noSleep))
	defer router.Close()

	var wg sync.WaitGroup
	for index := 0; index < 64; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := send(t.Context(), router, model, textRequest("hi"),
				nil, fmt.Sprintf("req-%d", index%4), "chat_completions")
			if err != nil {
				t.Errorf("send: %v", err)
				return
			}
			_ = result.Close()
		}(index)
	}
	wg.Wait()
}

func TestRouterCoolsDownRateLimitedProvider(t *testing.T) {
	t.Parallel()

	var limited atomic.Int32
	limitedServer := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		limited.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	liveServer := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	p1 := testProvider("p1", limitedServer.URL, "/chat/completions")
	p1.MaxRetries = 0
	p2 := testProvider("p2", liveServer.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model),
		WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	for attempt := 0; attempt < 2; attempt++ {
		result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
		if err != nil {
			t.Fatalf("send %d: %v", attempt, err)
		}
		if result.ProviderID != "p2" {
			t.Fatalf("send %d provider = %q", attempt, result.ProviderID)
		}
		_ = result.Close()
	}
	if got := limited.Load(); got != 1 {
		t.Fatalf("rate-limited provider attempts = %d, want 1", got)
	}
}
