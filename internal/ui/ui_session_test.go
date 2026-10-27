package ui

import (
	"testing"
	"time"
)

func TestUISessionExpiry(t *testing.T) {
	t.Parallel()

	store := newUIState()
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
