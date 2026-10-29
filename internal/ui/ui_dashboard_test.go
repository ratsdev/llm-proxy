package ui

import (
	"fmt"
	"testing"
	"time"
)

func TestUIHistorySnapshot(t *testing.T) {
	t.Parallel()

	t.Run("newest first", func(t *testing.T) {
		store := newUIState()
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
		store := newUIState()
		for index := 0; index < uiHistorySize+3; index++ {
			store.recordRequest(RequestRecord{User: "alice", RequestID: fmt.Sprint(index)})
		}

		if !store.historyFull() {
			t.Fatal("full history was not reported as full")
		}
		records := store.snapshotRecords("")
		if len(records) != uiHistorySize {
			t.Fatalf("history length = %d, want %d", len(records), uiHistorySize)
		}
		if records[0].RequestID != fmt.Sprint(uiHistorySize+2) {
			t.Fatalf("newest record = %q", records[0].RequestID)
		}
		if records[len(records)-1].RequestID != "3" {
			t.Fatalf("oldest record = %q", records[len(records)-1].RequestID)
		}
	})

	t.Run("includes every user", func(t *testing.T) {
		store := newUIState()
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

	summary, _, byModel, _ := summarizeUI(records, time.Now())
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

	summary, _, _, _ := summarizeUI([]RequestRecord{
		{Model: "model/a", Status: 200, DurationMS: 500, OutputTokens: 40},
	}, time.Now())
	if summary.AvgTTFTMS != 0 || summary.AvgOutputTPS != 0 {
		t.Fatalf("buffered-only window reported ttft=%v tps=%v", summary.AvgTTFTMS, summary.AvgOutputTPS)
	}
}
