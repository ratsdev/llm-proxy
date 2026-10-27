package core

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRegistrationSettingsDefaultsAndRoundTrip(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	settings, err := store.RegistrationSettings()
	if err != nil {
		t.Fatalf("RegistrationSettings() error = %v", err)
	}
	if settings.SelfRegistration {
		t.Fatal("self-registration defaulted to enabled")
	}
	if len(settings.DefaultAllowedModels) != 0 {
		t.Fatalf("default allowed models = %v, want none", settings.DefaultAllowedModels)
	}

	saved, err := store.SaveRegistrationSettings(RegistrationSettings{
		SelfRegistration:     true,
		DefaultAllowedModels: []string{"m1", "m1", ""},
	})
	if err != nil {
		t.Fatalf("SaveRegistrationSettings() error = %v", err)
	}
	if !saved.SelfRegistration {
		t.Fatal("saved settings lost the switch")
	}
	if len(saved.DefaultAllowedModels) != 1 || saved.DefaultAllowedModels[0] != "m1" {
		t.Fatalf("saved allowed models = %v", saved.DefaultAllowedModels)
	}

	reloaded, err := store.RegistrationSettings()
	if err != nil {
		t.Fatalf("RegistrationSettings() after save error = %v", err)
	}
	if !reloaded.SelfRegistration || len(reloaded.DefaultAllowedModels) != 1 || reloaded.DefaultAllowedModels[0] != "m1" {
		t.Fatalf("reloaded settings = %+v", reloaded)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.SelfRegistration || len(cfg.DefaultAllowedModels) != 1 || cfg.DefaultAllowedModels[0] != "m1" {
		t.Fatalf("config registration settings = %+v", cfg)
	}
}

func TestRegistrationSettingsWildcardReplacesList(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	saved, err := store.SaveRegistrationSettings(RegistrationSettings{
		SelfRegistration:     true,
		DefaultAllowedModels: []string{"m1", "*", "m2"},
	})
	if err != nil {
		t.Fatalf("SaveRegistrationSettings() error = %v", err)
	}
	if len(saved.DefaultAllowedModels) != 1 || saved.DefaultAllowedModels[0] != "*" {
		t.Fatalf("wildcard allow-list = %v, want the wildcard alone", saved.DefaultAllowedModels)
	}
}

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
