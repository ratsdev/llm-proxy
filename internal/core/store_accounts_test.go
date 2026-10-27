package core

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreAPIKeyLifecycleAndScoping(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	for _, id := range []string{"alice", "bob"} {
		if _, err := store.SaveUser(UserInput{ID: id, Name: id, Password: id + "-password"}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}

	first, err := store.CreateAPIKey("alice", "First")
	if err != nil {
		t.Fatalf("create first key: %v", err)
	}
	second, err := store.CreateAPIKey("alice", "")
	if err != nil {
		t.Fatalf("create second key: %v", err)
	}
	if second.Name != "API key" {
		t.Fatalf("default key name = %q", second.Name)
	}
	if first.Key == second.Key || first.ID == second.ID {
		t.Fatal("issued duplicate credentials")
	}

	keys, err := store.ListAPIKeys("alice")
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("alice key count = %d", len(keys))
	}
	alice, err := store.GetUser("alice")
	if err != nil {
		t.Fatalf("get alice: %v", err)
	}
	if alice.KeyCount != 2 {
		t.Fatalf("alice key count field = %d", alice.KeyCount)
	}
	if _, err := store.LoadConfig(); err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Deleting with the wrong owner must not remove the key.
	if err := store.DeleteAPIKey("bob", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete = %v", err)
	}
	if keys, err := store.ListAPIKeys("alice"); err != nil || len(keys) != 2 {
		t.Fatalf("keys after cross-user delete = %v, err=%v", keys, err)
	}
	if err := store.DeleteAPIKey("alice", first.ID); err != nil {
		t.Fatalf("delete own key: %v", err)
	}

	// Deleting the account removes every remaining key.
	if err := store.DeleteUser("alice"); err != nil {
		t.Fatalf("delete alice: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config after delete: %v", err)
	}
	if cfg.UsersByKey[second.Key] != nil {
		t.Fatal("deleted account's key survived")
	}
}

func TestStoreAPIKeyQuotaIsEnforced(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.SaveUser(UserInput{ID: "alice", Password: "alice-password"}); err != nil {
		t.Fatalf("save user: %v", err)
	}
	for index := 0; index < maxAPIKeysPerUser; index++ {
		if _, err := store.CreateAPIKey("alice", "key"); err != nil {
			t.Fatalf("create key %d: %v", index, err)
		}
	}
	if _, err := store.CreateAPIKey("alice", "overflow"); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("quota error = %v", err)
	}
	keys, err := store.ListAPIKeys("alice")
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != maxAPIKeysPerUser {
		t.Fatalf("key count = %d", len(keys))
	}
}
