package core

import (
	"errors"
	"path/filepath"
	"testing"
)

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
