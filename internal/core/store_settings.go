package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	settingSelfRegistration     = "self_registration"
	settingDefaultAllowedModels = "default_allowed_models"
)

// RegistrationSettings controls console self-service registration. The first
// administrator is always created through registration; these settings only
// govern member accounts created after that.
type RegistrationSettings struct {
	SelfRegistration     bool     `json:"self_registration"`
	DefaultAllowedModels []string `json:"default_allowed_models"`
}

// RegistrationSettings returns the stored registration policy with defaults
// applied: self-registration is off and new accounts receive no model access.
func (s *Store) RegistrationSettings() (RegistrationSettings, error) {
	settings := RegistrationSettings{DefaultAllowedModels: []string{}}
	enabled, err := s.settingValue(settingSelfRegistration)
	if err != nil {
		return RegistrationSettings{}, err
	}
	settings.SelfRegistration = strings.EqualFold(strings.TrimSpace(enabled), "true")

	rawModels, err := s.settingValue(settingDefaultAllowedModels)
	if err != nil {
		return RegistrationSettings{}, err
	}
	if err := decodeJSON(rawModels, &settings.DefaultAllowedModels); err != nil {
		return RegistrationSettings{}, fmt.Errorf("load %s setting: %w", settingDefaultAllowedModels, err)
	}
	settings.DefaultAllowedModels = normalizeDefaultModels(settings.DefaultAllowedModels)
	return settings, nil
}

// SaveRegistrationSettings validates and stores the registration policy. Both
// keys are written in one transaction so a failed save never leaves them
// half-written.
func (s *Store) SaveRegistrationSettings(input RegistrationSettings) (RegistrationSettings, error) {
	input.DefaultAllowedModels = normalizeDefaultModels(input.DefaultAllowedModels)
	encodedModels, err := encodeJSON(input.DefaultAllowedModels)
	if err != nil {
		return RegistrationSettings{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return RegistrationSettings{}, fmt.Errorf("save registration settings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?), (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		settingSelfRegistration, strconv.FormatBool(input.SelfRegistration),
		settingDefaultAllowedModels, encodedModels); err != nil {
		return RegistrationSettings{}, fmt.Errorf("save registration settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegistrationSettings{}, fmt.Errorf("save registration settings: %w", err)
	}
	return input, nil
}

func (s *Store) settingValue(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load %s setting: %w", key, err)
	}
	return value, nil
}

// normalizeDefaultModels trims the configured allow-list. The wildcard already
// means "every model, including future additions", so keeping individual IDs
// beside it would only make the stored value harder to interpret.
func normalizeDefaultModels(models []string) []string {
	normalized := normalizeStringList(models)
	for _, model := range normalized {
		if model == "*" {
			return []string{"*"}
		}
	}
	return normalized
}
