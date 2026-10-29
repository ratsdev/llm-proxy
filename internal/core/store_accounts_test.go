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

// Self-service registration.

func TestStoreRegisterUserUsesDefaultAccess(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	user, err := store.RegisterUser(UserInput{
		ID:       "member",
		Name:     "Member",
		Password: "member-password",
	}, []string{"m1", "*"})
	if err != nil {
		t.Fatalf("RegisterUser() error = %v", err)
	}
	if user.IsAdmin {
		t.Fatal("self-registered account was granted administrator rights")
	}
	if len(user.AllowedModels) != 2 || user.AllowedModels[0] != "m1" || user.AllowedModels[1] != "*" {
		t.Fatalf("allowed models = %v", user.AllowedModels)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	registered := cfg.UsersByID["member"]
	if registered == nil || registered.IsAdmin {
		t.Fatalf("registered member = %+v", registered)
	}
	if cfg.HasAdmin {
		t.Fatal("a member-only snapshot reports an administrator")
	}
	if !VerifyPassword(registered.PasswordHash, "member-password") {
		t.Fatal("registered password was not stored")
	}

	if _, err := store.RegisterUser(UserInput{ID: "member", Password: "another-password"}, nil); !errors.Is(err, ErrUserIDTaken) {
		t.Fatalf("duplicate user id error = %v, want ErrUserIDTaken", err)
	}
	if _, err := store.RegisterUser(UserInput{ID: "short", Password: "short"}, nil); err == nil {
		t.Fatal("short password was accepted")
	}
}

// First-administrator registration.

func TestStoreRegisterFirstAdmin(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	hasAdmin, err := store.HasAdmin()
	if err != nil {
		t.Fatalf("HasAdmin() error = %v", err)
	}
	if hasAdmin {
		t.Fatal("a fresh database already has an administrator")
	}

	user, err := store.RegisterAdmin(UserInput{ID: "owner", Name: "Owner", Password: "owner-password"})
	if err != nil {
		t.Fatalf("RegisterAdmin() error = %v", err)
	}
	if user.ID != "owner" || !user.IsAdmin || user.Name != "Owner" {
		t.Fatalf("registered administrator = %+v", user)
	}
	if len(user.AllowedModels) != 1 || user.AllowedModels[0] != "*" {
		t.Fatalf("allowed models = %v, want the administrator wildcard", user.AllowedModels)
	}

	hasAdmin, err = store.HasAdmin()
	if err != nil || !hasAdmin {
		t.Fatalf("HasAdmin() = %v, err=%v", hasAdmin, err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	admin := cfg.UsersByID["owner"]
	if admin == nil || !admin.IsAdmin {
		t.Fatalf("registered administrator missing from config: %+v", cfg.UsersByID)
	}
	if !cfg.HasAdmin {
		t.Fatal("configuration snapshot does not report the administrator")
	}
	if !VerifyPassword(admin.PasswordHash, "owner-password") {
		t.Fatal("registered password was not stored")
	}
	if VerifyPassword(admin.PasswordHash, "wrong-password") {
		t.Fatal("a wrong password was accepted")
	}

	// Registration closes as soon as an administrator exists.
	if _, err := store.RegisterAdmin(UserInput{ID: "second", Password: "second-password"}); !errors.Is(err, ErrAdminExists) {
		t.Fatalf("second RegisterAdmin() error = %v, want ErrAdminExists", err)
	}
}

func TestStoreRegisterAdminPersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "proxy.db")
	store := openTestStore(t, path)
	if _, err := store.RegisterAdmin(UserInput{ID: "owner", Password: "persisted-password"}); err != nil {
		t.Fatalf("RegisterAdmin() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := openTestStore(t, path)
	hasAdmin, err := reopened.HasAdmin()
	if err != nil || !hasAdmin {
		t.Fatalf("reopened HasAdmin() = %v, err=%v", hasAdmin, err)
	}
	if _, err := reopened.RegisterAdmin(UserInput{ID: "other", Password: "other-password"}); !errors.Is(err, ErrAdminExists) {
		t.Fatalf("reopened registration error = %v, want ErrAdminExists", err)
	}
}

func TestStoreRegisterAdminValidatesAccount(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.RegisterAdmin(UserInput{ID: "owner", Password: "short"}); err == nil {
		t.Fatal("short password was accepted")
	}
	if _, err := store.RegisterAdmin(UserInput{ID: " owner ", Password: "owner-password"}); err == nil {
		t.Fatal("user id with surrounding whitespace was accepted")
	}

	// A pre-existing member with the same id cannot be taken over by
	// registration, but registration itself stays open.
	if _, err := store.SaveUser(UserInput{ID: "member", Password: "member-password"}); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
	if _, err := store.RegisterAdmin(UserInput{ID: "member", Password: "member-password"}); !errors.Is(err, ErrUserIDTaken) {
		t.Fatalf("registering an existing member id error = %v, want ErrUserIDTaken", err)
	}
	if _, err := store.RegisterAdmin(UserInput{ID: "owner", Password: "owner-password"}); err != nil {
		t.Fatalf("RegisterAdmin() after a member existed error = %v", err)
	}
}

// The administrator account is not editable through the member endpoints.
func TestStoreAdministratorIsProtectedFromUserEndpoints(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.RegisterAdmin(UserInput{ID: "owner", Name: "Owner", Password: "owner-password"}); err != nil {
		t.Fatalf("RegisterAdmin() error = %v", err)
	}
	if _, err := store.SaveUser(UserInput{ID: "owner", Name: "Renamed"}); err == nil {
		t.Fatal("administrator was editable through SaveUser")
	}
	if err := store.DeleteUser("owner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteUser(administrator) error = %v, want ErrNotFound", err)
	}

	// The user endpoints can never grant the administrator role.
	if _, err := store.SaveUser(UserInput{ID: "member", Password: "member-password", IsAdmin: true}); err != nil {
		t.Fatalf("SaveUser(member) error = %v", err)
	}
	member, err := store.GetUser("member")
	if err != nil {
		t.Fatalf("GetUser(member) error = %v", err)
	}
	if member.IsAdmin {
		t.Fatal("SaveUser() granted administrator rights")
	}
}
