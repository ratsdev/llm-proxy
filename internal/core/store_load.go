package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// LoadConfig builds an immutable configuration snapshot from the database.
func (s *Store) LoadConfig() (*Config, error) {
	providers, err := s.loadProviders()
	if err != nil {
		return nil, err
	}
	models, err := s.loadModels(providers)
	if err != nil {
		return nil, err
	}
	usersByID, usersByKey, err := s.loadUsers()
	if err != nil {
		return nil, err
	}
	rawDropParams, err := s.settingValue("drop_params")
	if err != nil {
		return nil, err
	}
	registration, err := s.RegistrationSettings()
	if err != nil {
		return nil, err
	}
	hasAdmin := false
	for _, user := range usersByID {
		if user.IsAdmin {
			hasAdmin = true
			break
		}
	}
	return &Config{
		Providers:            providers,
		Models:               models,
		UsersByKey:           usersByKey,
		UsersByID:            usersByID,
		DropParams:           strings.EqualFold(strings.TrimSpace(rawDropParams), "true"),
		SelfRegistration:     registration.SelfRegistration,
		HasAdmin:             hasAdmin,
		DefaultAllowedModels: registration.DefaultAllowedModels,
	}, nil
}

func (s *Store) loadProviders() (map[string]*Provider, error) {
	inputs, err := s.listProviderInputs()
	if err != nil {
		return nil, err
	}
	providers := make(map[string]*Provider, len(inputs))
	for _, input := range inputs {
		provider, err := buildProvider(input)
		if err != nil {
			return nil, err
		}
		providers[provider.ID] = provider
	}
	return providers, nil
}

// listProviderInputs reads every provider row in a stable order. It is shared
// by configuration loading and the Web UI list endpoint so both interpret the
// stored columns identically.
func (s *Store) listProviderInputs() ([]ProviderInput, error) {
	rows, err := s.db.Query(`SELECT ` + providerFields + ` FROM providers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	inputs := make([]ProviderInput, 0)
	for rows.Next() {
		input, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("load providers: %w", err)
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load providers: %w", err)
	}
	return inputs, nil
}

func (s *Store) loadModels(providers map[string]*Provider) (map[string]*Model, error) {
	inputs, err := s.listModelInputs()
	if err != nil {
		return nil, err
	}
	models := make(map[string]*Model, len(inputs))
	for _, input := range inputs {
		model, err := buildModel(input)
		if err != nil {
			return nil, err
		}
		for _, binding := range model.Providers {
			if _, ok := providers[binding.ProviderID]; !ok {
				return nil, fmt.Errorf("model %q references unknown provider %q", model.ID, binding.ProviderID)
			}
		}
		models[model.ID] = model
	}
	return models, nil
}

// listModelInputs reads every model and its provider bindings. The rows must be
// closed before the binding queries because the store uses a single connection.
func (s *Store) listModelInputs() ([]ModelInput, error) {
	rows, err := s.db.Query(`SELECT ` + modelFields + ` FROM models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load models: %w", err)
	}
	defer func() { _ = rows.Close() }()

	inputs := make([]ModelInput, 0)
	for rows.Next() {
		input, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("load models: %w", err)
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load models: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("load models: %w", err)
	}

	for index := range inputs {
		bindings, err := s.loadModelProviders(inputs[index].ID)
		if err != nil {
			return nil, err
		}
		inputs[index].Providers = bindings
	}
	return inputs, nil
}

func (s *Store) loadModelProviders(modelID string) ([]ModelProviderInput, error) {
	rows, err := s.db.Query(`SELECT provider_id, upstream_model, max_retry_after
		FROM model_providers WHERE model_id = ? ORDER BY position`, modelID)
	if err != nil {
		return nil, fmt.Errorf("load model providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	bindings := make([]ModelProviderInput, 0)
	for rows.Next() {
		var (
			binding ModelProviderInput
			retry   sql.NullFloat64
		)
		if err := rows.Scan(&binding.ProviderID, &binding.UpstreamModel, &retry); err != nil {
			return nil, fmt.Errorf("scan model provider: %w", err)
		}
		if retry.Valid {
			binding.MaxRetryAfter = &retry.Float64
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load model providers: %w", err)
	}
	return bindings, nil
}

func (s *Store) loadUsers() (map[string]*User, map[string]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userFields + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("load users: %w", err)
	}

	byID := make(map[string]*User)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		byID[user.ID] = user
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, fmt.Errorf("load users: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	keyRows, err := s.db.Query(`SELECT user_id, key FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, nil, fmt.Errorf("load api keys: %w", err)
	}
	defer func() { _ = keyRows.Close() }()

	byKey := make(map[string]*User)
	for keyRows.Next() {
		var (
			userID string
			key    string
		)
		if err := keyRows.Scan(&userID, &key); err != nil {
			return nil, nil, fmt.Errorf("scan api key: %w", err)
		}
		user, ok := byID[userID]
		if !ok {
			return nil, nil, fmt.Errorf("api key references unknown user %q", userID)
		}
		if key == "" {
			continue
		}
		if _, exists := byKey[key]; exists {
			return nil, nil, fmt.Errorf("duplicate api key for user %q", userID)
		}
		byKey[key] = user
	}
	if err := keyRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load api keys: %w", err)
	}
	return byID, byKey, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*User, error) {
	var (
		user    User
		allowed string
		isAdmin int
	)
	if err := row.Scan(&user.ID, &user.Name, &allowed, &isAdmin, &user.PasswordHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	user.IsAdmin = isAdmin != 0
	if err := decodeJSON(allowed, &user.AllowedModels); err != nil {
		return nil, fmt.Errorf("user %s allowed_models: %w", user.ID, err)
	}
	return &user, nil
}
