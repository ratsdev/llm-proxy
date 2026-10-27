package ui

import (
	"fmt"
	"testing"
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
