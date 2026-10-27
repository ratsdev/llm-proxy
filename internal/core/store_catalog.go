package core

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// ------------------------------------------------------------------ providers

// GetProvider returns one provider.
func (s *Store) GetProvider(id string) (ProviderInput, error) {
	row := s.db.QueryRow(`SELECT `+providerFields+` FROM providers WHERE id = ?`, id)
	return scanProvider(row)
}

func scanProvider(row rowScanner) (ProviderInput, error) {
	var (
		input          ProviderInput
		endpoints      string
		headers        string
		upstreamModels string
		timeout        sql.NullFloat64
		retries        int
	)
	if err := row.Scan(&input.ID, &input.Name, &input.BaseURL, &input.APIKey, &retries, &timeout, &endpoints, &headers, &input.Proxy, &upstreamModels); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProviderInput{}, ErrNotFound
		}
		return ProviderInput{}, err
	}
	if timeout.Valid {
		input.Timeout = &timeout.Float64
	}
	input.MaxRetries = &retries
	if err := decodeJSON(headers, &input.Headers); err != nil {
		return ProviderInput{}, fmt.Errorf("provider %s headers: %w", input.ID, err)
	}
	if err := decodeJSON(endpoints, &input.Endpoints); err != nil {
		return ProviderInput{}, fmt.Errorf("provider %s endpoints: %w", input.ID, err)
	}
	if err := decodeJSON(upstreamModels, &input.UpstreamModels); err != nil {
		return ProviderInput{}, fmt.Errorf("provider %s upstream_models: %w", input.ID, err)
	}
	return input, nil
}

// SaveProvider validates and upserts a provider. A blank API key on an update
// keeps the stored credential, which is what the console's edit form promises;
// a new provider with a blank key has no upstream credential.
func (s *Store) SaveProvider(input ProviderInput) (ProviderInput, error) {
	provider, err := buildProvider(input)
	if err != nil {
		return ProviderInput{}, err
	}
	if provider.APIKey == "" {
		var existing string
		err := s.db.QueryRow(`SELECT api_key FROM providers WHERE id = ?`, provider.ID).Scan(&existing)
		switch {
		case err == nil:
			provider.APIKey = existing
		case !errors.Is(err, sql.ErrNoRows):
			return ProviderInput{}, fmt.Errorf("read provider %s: %w", provider.ID, err)
		}
	}
	input.ID = provider.ID
	input.Name = provider.Name
	input.BaseURL = provider.BaseURL
	input.APIKey = provider.APIKey
	input.Proxy = provider.Proxy
	if input.MaxRetries == nil {
		retries := provider.MaxRetries
		input.MaxRetries = &retries
	}
	input.Endpoints = endpointConfig(provider.Endpoints)
	input.UpstreamModels = provider.UpstreamModels
	if input.Headers == nil {
		input.Headers = map[string]string{}
	}
	if err := s.validateProviderCatalog(provider); err != nil {
		return ProviderInput{}, err
	}

	endpoints, err := encodeJSON(input.Endpoints)
	if err != nil {
		return ProviderInput{}, err
	}
	headers, err := encodeJSON(provider.Headers)
	if err != nil {
		return ProviderInput{}, err
	}
	upstreamModels, err := encodeJSON(provider.UpstreamModels)
	if err != nil {
		return ProviderInput{}, err
	}
	_, err = s.db.Exec(`
		INSERT INTO providers (id, name, base_url, api_key, max_retries, timeout, endpoints, headers, proxy, upstream_models)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			base_url = excluded.base_url,
			api_key = excluded.api_key,
			max_retries = excluded.max_retries,
			timeout = excluded.timeout,
			endpoints = excluded.endpoints,
			headers = excluded.headers,
			proxy = excluded.proxy,
			upstream_models = excluded.upstream_models`,
		provider.ID, provider.Name, provider.BaseURL, provider.APIKey, provider.MaxRetries,
		util.Nullable(provider.Timeout), endpoints, headers, provider.Proxy, upstreamModels)
	if err != nil {
		return ProviderInput{}, fmt.Errorf("save provider: %w", err)
	}
	return input, nil
}

// validateProviderCatalog keeps an edited provider catalog consistent with the
// model bindings that already reference it. Clearing the catalog is allowed and
// means "no restriction"; a non-empty catalog must still contain every upstream
// model that is currently bound.
func (s *Store) validateProviderCatalog(provider *Provider) error {
	if len(provider.UpstreamModels) == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT upstream_model FROM model_providers WHERE provider_id = ? ORDER BY upstream_model`, provider.ID)
	if err != nil {
		return fmt.Errorf("validate provider %s catalog: %w", provider.ID, err)
	}
	bound := make([]string, 0)
	for rows.Next() {
		var upstreamModel string
		if err := rows.Scan(&upstreamModel); err != nil {
			_ = rows.Close()
			return fmt.Errorf("validate provider %s catalog: %w", provider.ID, err)
		}
		bound = append(bound, upstreamModel)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("validate provider %s catalog: %w", provider.ID, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("validate provider %s catalog: %w", provider.ID, err)
	}
	for _, upstreamModel := range bound {
		if !slices.Contains(provider.UpstreamModels, upstreamModel) {
			return fmt.Errorf(
				"providers.%s.upstream_models must include %q because a model binding still references it",
				provider.ID, upstreamModel)
		}
	}
	return nil
}

// DeleteProvider removes an unused provider.
func (s *Store) DeleteProvider(id string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM model_providers WHERE provider_id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("provider %q is still bound to %d model(s)", id, count)
	}
	result, err := s.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// --------------------------------------------------------------------- models

// GetModel returns one model.
func (s *Store) GetModel(id string) (ModelInput, error) {
	row := s.db.QueryRow(`SELECT `+modelFields+` FROM models WHERE id = ?`, id)
	input, err := scanModel(row)
	if err != nil {
		return ModelInput{}, err
	}
	bindings, err := s.loadModelProviders(id)
	if err != nil {
		return ModelInput{}, err
	}
	input.Providers = bindings
	return input, nil
}

func scanModel(row rowScanner) (ModelInput, error) {
	var (
		input            ModelInput
		contextLength    sql.NullInt64
		maxOutputLength  sql.NullInt64
		modalities       string
		outputModalities string
		reasoning        int
		tools            int
		timeout          sql.NullFloat64
	)
	if err := row.Scan(&input.ID, &input.Name, &input.Routing, &contextLength, &maxOutputLength, &modalities, &outputModalities, &reasoning, &tools, &timeout); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ModelInput{}, ErrNotFound
		}
		return ModelInput{}, err
	}
	if contextLength.Valid {
		value := int(contextLength.Int64)
		input.ContextLength = &value
	}
	if maxOutputLength.Valid {
		value := int(maxOutputLength.Int64)
		input.MaxOutputLength = &value
	}
	input.Reasoning = reasoning != 0
	input.Tools = tools != 0
	if timeout.Valid {
		input.Timeout = &timeout.Float64
	}
	if err := decodeJSON(modalities, &input.Modalities); err != nil {
		return ModelInput{}, fmt.Errorf("model %s modalities: %w", input.ID, err)
	}
	if err := decodeJSON(outputModalities, &input.OutputModalities); err != nil {
		return ModelInput{}, fmt.Errorf("model %s output_modalities: %w", input.ID, err)
	}
	return input, nil
}

// SaveModel validates and upserts a model and its provider bindings.
func (s *Store) SaveModel(input ModelInput) (ModelInput, error) {
	model, err := buildModel(input)
	if err != nil {
		return ModelInput{}, err
	}
	for _, binding := range model.Providers {
		var upstreamModels string
		err := s.db.QueryRow(`SELECT upstream_models FROM providers WHERE id = ?`, binding.ProviderID).Scan(&upstreamModels)
		if errors.Is(err, sql.ErrNoRows) {
			return ModelInput{}, fmt.Errorf("models.%s references unknown provider %q", model.ID, binding.ProviderID)
		}
		if err != nil {
			return ModelInput{}, err
		}
		var catalog []string
		if err := decodeJSON(upstreamModels, &catalog); err != nil {
			return ModelInput{}, fmt.Errorf("provider %s upstream_models: %w", binding.ProviderID, err)
		}
		if len(catalog) > 0 && !slices.Contains(catalog, binding.UpstreamModel) {
			return ModelInput{}, fmt.Errorf(
				"models.%s binding to provider %q uses upstream model %q, which the provider does not declare",
				model.ID, binding.ProviderID, binding.UpstreamModel)
		}
	}
	modalities, err := encodeJSON(model.Modalities)
	if err != nil {
		return ModelInput{}, err
	}
	outputModalities, err := encodeJSON(model.OutputModalities)
	if err != nil {
		return ModelInput{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ModelInput{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		INSERT INTO models (id, name, routing, context_length, max_output_length, modalities, output_modalities, reasoning, tools, timeout)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			routing = excluded.routing,
			context_length = excluded.context_length,
			max_output_length = excluded.max_output_length,
			modalities = excluded.modalities,
			output_modalities = excluded.output_modalities,
			reasoning = excluded.reasoning,
			tools = excluded.tools,
			timeout = excluded.timeout`,
		model.ID, model.Name, model.Routing, util.Nullable(model.ContextLength), util.Nullable(model.MaxOutputLength),
		modalities, outputModalities, boolInt(model.Reasoning), boolInt(model.Tools), util.Nullable(model.Timeout)); err != nil {
		return ModelInput{}, fmt.Errorf("save model: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM model_providers WHERE model_id = ?`, model.ID); err != nil {
		return ModelInput{}, err
	}
	for position, binding := range model.Providers {
		if _, err := tx.Exec(`
			INSERT INTO model_providers (model_id, provider_id, upstream_model, max_retry_after, position)
			VALUES (?, ?, ?, ?, ?)`,
			model.ID, binding.ProviderID, binding.UpstreamModel,
			util.Nullable(binding.MaxRetryAfter), position); err != nil {
			return ModelInput{}, fmt.Errorf("save model provider: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ModelInput{}, err
	}
	return modelInput(model), nil
}

// DeleteModel removes a model and its bindings.
func (s *Store) DeleteModel(id string) error {
	result, err := s.db.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
