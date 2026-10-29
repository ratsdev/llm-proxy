// Non-HTTP console tests: the embedded bundle, the in-memory dashboard window,
// the provider view and the session store. The JSON API is covered in
// api_test.go.

package ui

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// Embedded bundle.

// The console renders a fallback converter list when the API is unavailable, so
// the bundle and the Go registry must agree; otherwise an operator edits a
// provider with a converter the server would reject (or misses a new one).
func TestEmbeddedConsoleListsEveryConverter(t *testing.T) {
	t.Parallel()

	bundle, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	for _, definition := range convert.Definitions() {
		if !strings.Contains(string(bundle), definition.ID) {
			t.Errorf("console bundle does not mention converter %q", definition.ID)
		}
	}
	for _, format := range convert.Formats() {
		if !strings.Contains(string(bundle), string(format)) {
			t.Errorf("console bundle does not mention wire format %q", format)
		}
	}
}

func TestEmbeddedConsoleServesIndexAndAssets(t *testing.T) {
	t.Parallel()

	if !strings.Contains(string(indexHTML), "/assets/app.js") {
		t.Fatal("index.html does not load the console bundle")
	}
	for _, name := range []string{"app.js", "format.js", "styles.css"} {
		if _, ok := assetETags[name]; !ok {
			t.Errorf("asset %q has no ETag entry", name)
		}
	}
}

// Dashboard window.

func TestUIHistorySnapshot(t *testing.T) {
	t.Parallel()

	t.Run("newest first", func(t *testing.T) {
		store := newState()
		for index := 0; index < 3; index++ {
			store.recordRequest(RequestRecord{User: "alice", RequestID: fmt.Sprint(index)})
		}

		if store.historyFull() {
			t.Fatal("partial history was reported as full")
		}
		records := store.snapshotRecords("")
		if len(records) != 3 {
			t.Fatalf("history length = %d, want 3", len(records))
		}
		if records[0].RequestID != "2" || records[2].RequestID != "0" {
			t.Fatalf("history order = %v", []string{records[0].RequestID, records[1].RequestID, records[2].RequestID})
		}
	})

	t.Run("ring buffer", func(t *testing.T) {
		store := newState()
		for index := 0; index < historySize+3; index++ {
			store.recordRequest(RequestRecord{User: "alice", RequestID: fmt.Sprint(index)})
		}

		if !store.historyFull() {
			t.Fatal("full history was not reported as full")
		}
		records := store.snapshotRecords("")
		if len(records) != historySize {
			t.Fatalf("history length = %d, want %d", len(records), historySize)
		}
		if records[0].RequestID != fmt.Sprint(historySize+2) {
			t.Fatalf("newest record = %q", records[0].RequestID)
		}
		if records[len(records)-1].RequestID != "3" {
			t.Fatalf("oldest record = %q", records[len(records)-1].RequestID)
		}
	})

	t.Run("includes every user", func(t *testing.T) {
		store := newState()
		store.recordRequest(RequestRecord{User: "alice", RequestID: "a1"})
		store.recordRequest(RequestRecord{User: "bob", RequestID: "b1"})
		store.recordRequest(RequestRecord{User: "alice", RequestID: "a2"})

		records := store.snapshotRecords("")
		if len(records) != 3 || records[0].RequestID != "a2" || records[1].RequestID != "b1" || records[2].RequestID != "a1" {
			t.Fatalf("history = %v", records)
		}
	})
}

// TTFT and decode throughput describe replies the gateway actually streamed:
// a buffered JSON body records no TTFT, and a failed request never measures a
// generation rate, so both stay out of the averages.
func TestSummarizeUILatency(t *testing.T) {
	t.Parallel()

	records := []RequestRecord{
		{Model: "model/a", Stream: true, Status: 200, DurationMS: 1200, TTFTMS: 200, OutputTokens: 100},
		{Model: "model/a", Stream: true, Status: 200, DurationMS: 2400, TTFTMS: 400, OutputTokens: 200},
		{Model: "model/b", Stream: true, Status: 200, DurationMS: 1800, TTFTMS: 300, OutputTokens: 150},
		{Model: "model/b", Stream: false, Status: 200, DurationMS: 900, OutputTokens: 500},
		{Model: "model/a", Stream: true, Status: 500, DurationMS: 1000, TTFTMS: 100, OutputTokens: 999},
	}

	summary, _, byModel, _ := summarize(records, time.Now())
	if summary.AvgTTFTMS != 300 {
		t.Fatalf("avg ttft = %v, want 300", summary.AvgTTFTMS)
	}
	if summary.AvgOutputTPS != 100 {
		t.Fatalf("avg output tps = %v, want 100", summary.AvgOutputTPS)
	}
	if summary.AvgDurationMS != 1460 {
		t.Fatalf("avg duration = %v, want 1460", summary.AvgDurationMS)
	}

	if len(byModel) != 2 {
		t.Fatalf("breakdown = %v", byModel)
	}
	first := byModel[0]
	if first.Name != "model/a" || first.AvgTTFTMS != 300 || first.AvgOutputTPS != 100 {
		t.Fatalf("model/a = %+v", first)
	}
	second := byModel[1]
	if second.Name != "model/b" || second.AvgTTFTMS != 300 || second.AvgOutputTPS != 100 {
		t.Fatalf("model/b = %+v", second)
	}
}

func TestSummarizeUIWithoutStreamingSamples(t *testing.T) {
	t.Parallel()

	summary, _, _, _ := summarize([]RequestRecord{
		{Model: "model/a", Status: 200, DurationMS: 500, OutputTokens: 40},
	}, time.Now())
	if summary.AvgTTFTMS != 0 || summary.AvgOutputTPS != 0 {
		t.Fatalf("buffered-only window reported ttft=%v tps=%v", summary.AvgTTFTMS, summary.AvgOutputTPS)
	}
}

// Provider view.

// A model can bind the same provider twice with different upstream names. The
// provider view must still list that model once.
func TestProviderModelsDeduplicatesRepeatedBindings(t *testing.T) {
	t.Parallel()

	model := &Model{ID: "alias", Providers: []ModelProvider{
		{ProviderID: "p", UpstreamModel: "up-a"},
		{ProviderID: "p", UpstreamModel: "up-b"},
	}}
	cfg := &Config{
		Providers: map[string]*Provider{"p": {ID: "p"}},
		Models:    map[string]*Model{"alias": model},
	}
	grouped := providerModels(cfg)
	if ids := grouped["p"]; len(ids) != 1 || ids[0].ID != "alias" {
		t.Fatalf("provider models = %v", ids)
	}
}

// Sessions.

func TestUISessionExpiry(t *testing.T) {
	t.Parallel()

	store := newState()
	now := time.Now()
	token, expires, err := store.createSession("alice", now)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if _, ok := store.lookupSession(token, now.Add(time.Minute)); !ok {
		t.Fatal("session missing before expiry")
	}
	if _, ok := store.lookupSession(token, expires.Add(time.Second)); ok {
		t.Fatal("session still valid after expiry")
	}
	if _, ok := store.lookupSession(token, now); ok {
		t.Fatal("expired session was not pruned")
	}
}
