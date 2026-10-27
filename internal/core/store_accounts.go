package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// ---------------------------------------------------------------------- users

// ListUsers returns every account with its API key count. Password hashes are
// never returned.
func (s *Store) ListUsers() ([]UserInput, error) {
	rows, err := s.db.Query(`SELECT ` + userListFields + ` FROM users u ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]UserInput, 0)
	for rows.Next() {
		input, err := scanUserInput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, input)
	}
	return out, rows.Err()
}

// GetUser returns one account.
func (s *Store) GetUser(id string) (UserInput, error) {
	row := s.db.QueryRow(`SELECT `+userListFields+` FROM users u WHERE u.id = ?`, id)
	return scanUserInput(row)
}

func scanUserInput(row rowScanner) (UserInput, error) {
	var (
		input   UserInput
		allowed string
		isAdmin int
	)
	if err := row.Scan(&input.ID, &input.Name, &allowed, &isAdmin, &input.KeyCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserInput{}, ErrNotFound
		}
		return UserInput{}, err
	}
	input.IsAdmin = isAdmin != 0
	if err := decodeJSON(allowed, &input.AllowedModels); err != nil {
		return UserInput{}, fmt.Errorf("user %s allowed_models: %w", input.ID, err)
	}
	return input, nil
}

// HasAdmin reports whether an administrator account exists.
func (s *Store) HasAdmin() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&count); err != nil {
		return false, fmt.Errorf("count administrators: %w", err)
	}
	return count > 0, nil
}

// RegisterAdmin creates the first administrator account. Registration is only
// open while no administrator exists; the condition is part of the insert so
// two concurrent first visits cannot create two administrators. The account
// name comes from the operator, not a fixed "admin" login.
func (s *Store) RegisterAdmin(input UserInput) (UserInput, error) {
	if exists, err := s.HasAdmin(); err != nil {
		return UserInput{}, err
	} else if exists {
		return UserInput{}, ErrAdminExists
	}
	record, err := s.prepareRegistration(input, []string{"*"})
	if err != nil {
		return UserInput{}, err
	}
	result, err := s.db.Exec(`
		INSERT INTO users (id, name, allowed_models, is_admin, password_hash, created_at, updated_at)
		SELECT ?, ?, ?, 1, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE is_admin = 1)
		ON CONFLICT(id) DO NOTHING`,
		record.ID, record.Name, record.AllowedJSON, record.PasswordHash, record.CreatedAt, record.CreatedAt)
	if err != nil {
		return UserInput{}, fmt.Errorf("create administrator: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		// A concurrent first visit or an account created directly in the store
		// may have won the race; report which condition closed registration.
		if exists, checkErr := s.HasAdmin(); checkErr != nil {
			return UserInput{}, checkErr
		} else if exists {
			return UserInput{}, ErrAdminExists
		}
		return UserInput{}, fmt.Errorf("%w: %q", ErrUserIDTaken, record.ID)
	}
	return UserInput{
		ID:            record.ID,
		Name:          record.Name,
		AllowedModels: record.AllowedModels,
		IsAdmin:       true,
	}, nil
}

// RegisterUser creates a self-service member account with the supplied model
// allow-list. It never grants administrator rights; that role is reserved for
// the first registration.
func (s *Store) RegisterUser(input UserInput, allowedModels []string) (UserInput, error) {
	record, err := s.prepareRegistration(input, allowedModels)
	if err != nil {
		return UserInput{}, err
	}
	result, err := s.db.Exec(`
		INSERT INTO users (id, name, allowed_models, is_admin, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		record.ID, record.Name, record.AllowedJSON, record.PasswordHash, record.CreatedAt, record.CreatedAt)
	if err != nil {
		return UserInput{}, fmt.Errorf("create user: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return UserInput{}, fmt.Errorf("%w: %q", ErrUserIDTaken, record.ID)
	}
	return UserInput{
		ID:            record.ID,
		Name:          record.Name,
		AllowedModels: record.AllowedModels,
	}, nil
}

// preparedAccount is one row ready to be inserted by an account registration.
type preparedAccount struct {
	ID            string
	Name          string
	AllowedModels []string
	AllowedJSON   string
	PasswordHash  string
	CreatedAt     string
}

// prepareRegistration validates the input shared by both registration paths.
// The duplicate-id check runs before hashing so a taken name is rejected
// without a PBKDF2 derivation.
func (s *Store) prepareRegistration(input UserInput, allowedModels []string) (preparedAccount, error) {
	id, err := configID("user", input.ID)
	if err != nil {
		return preparedAccount{}, err
	}
	if len(input.Password) < MinPasswordLength {
		return preparedAccount{}, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	var taken int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&taken); err != nil {
		return preparedAccount{}, err
	}
	if taken > 0 {
		return preparedAccount{}, fmt.Errorf("%w: %q", ErrUserIDTaken, id)
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return preparedAccount{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	allowed := normalizeStringList(allowedModels)
	encodedAllowed, err := encodeJSON(allowed)
	if err != nil {
		return preparedAccount{}, err
	}
	return preparedAccount{
		ID:            id,
		Name:          name,
		AllowedModels: allowed,
		AllowedJSON:   encodedAllowed,
		PasswordHash:  passwordHash,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// SaveUser validates and upserts a console account. A new account requires a
// password of at least eight characters; an update with a blank password keeps
// the existing one.
func (s *Store) SaveUser(input UserInput) (UserInput, error) {
	id, err := configID("user", input.ID)
	if err != nil {
		return UserInput{}, err
	}
	input.ID = id
	input.Name = strings.TrimSpace(input.Name)

	var (
		existingHash    string
		existingIsAdmin int
	)
	err = s.db.QueryRow(`SELECT is_admin, password_hash FROM users WHERE id = ?`, id).
		Scan(&existingIsAdmin, &existingHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return UserInput{}, err
	}
	if err == nil && existingIsAdmin != 0 {
		return UserInput{}, fmt.Errorf("administrator accounts cannot be edited here")
	}
	if input.Password != "" {
		if len(input.Password) < MinPasswordLength {
			return UserInput{}, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
		}
		existingHash, err = hashPassword(input.Password)
		if err != nil {
			return UserInput{}, err
		}
	} else if existingHash == "" {
		return UserInput{}, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	input.Password = ""
	// Administrator accounts are only created by first-run registration, so
	// the user endpoints can never grant that role.
	input.IsAdmin = false

	allowed := input.AllowedModels
	if allowed == nil {
		allowed = []string{}
	}
	encodedAllowed, err := encodeJSON(allowed)
	if err != nil {
		return UserInput{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		INSERT INTO users (id, name, allowed_models, is_admin, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			allowed_models = excluded.allowed_models,
			is_admin = excluded.is_admin,
			password_hash = excluded.password_hash,
			updated_at = excluded.updated_at`,
		id, input.Name, encodedAllowed, boolInt(input.IsAdmin), existingHash, now, now)
	if err != nil {
		return UserInput{}, fmt.Errorf("save user: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, id).Scan(&input.KeyCount); err != nil {
		return UserInput{}, err
	}
	return input, nil
}

// DeleteUser removes a non-administrator account. Its API keys are deleted by
// the foreign-key cascade.
func (s *Store) DeleteUser(id string) error {
	result, err := s.db.Exec(`DELETE FROM users WHERE id = ? AND is_admin = 0`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------------ api keys

const maxAPIKeysPerUser = 25

// ListAPIKeys returns one account's keys, newest first.
func (s *Store) ListAPIKeys(userID string) ([]APIKeyInput, error) {
	rows, err := s.db.Query(`SELECT id, user_id, name, key, created_at
		FROM api_keys WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]APIKeyInput, 0)
	for rows.Next() {
		var key APIKeyInput
		if err := rows.Scan(&key.ID, &key.UserID, &key.Name, &key.Key, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// CreateAPIKey issues a new gateway credential for one account.
func (s *Store) CreateAPIKey(userID, name string) (APIKeyInput, error) {
	userID = strings.TrimSpace(userID)
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return APIKeyInput{}, err
	}
	if exists == 0 {
		return APIKeyInput{}, ErrNotFound
	}
	limitError := fmt.Errorf("an account can have at most %d API keys", maxAPIKeysPerUser)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "API key"
	}
	if len(name) > 80 {
		if runes := []rune(name); len(runes) > 80 {
			name = string(runes[:80])
		}
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	for attempt := 0; attempt < 3; attempt++ {
		// Both values authenticate or identify a credential, so a failed entropy
		// source aborts the request instead of storing a predictable key.
		identifier, err := randomKeyID()
		if err != nil {
			return APIKeyInput{}, err
		}
		secret, err := randomAPIKey()
		if err != nil {
			return APIKeyInput{}, err
		}
		input := APIKeyInput{
			ID:        identifier,
			UserID:    userID,
			Name:      name,
			Key:       secret,
			CreatedAt: createdAt,
		}
		// Generated identifiers are overwhelmingly unique, but a collision must
		// be retried with a fresh value rather than surfaced as a database error.
		// The pre-check is race-free because the store uses one connection, so no
		// other statement can insert between it and the statement below.
		var taken int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE id = ? OR key = ?`,
			input.ID, input.Key).Scan(&taken); err != nil {
			return APIKeyInput{}, fmt.Errorf("create api key: %w", err)
		}
		if taken > 0 {
			continue
		}
		// The quota check is part of the insert so two concurrent creates cannot
		// both observe room for one more key.
		result, err := s.db.Exec(`INSERT INTO api_keys (id, user_id, name, key, created_at)
			SELECT ?, ?, ?, ?, ?
			WHERE (SELECT COUNT(*) FROM api_keys WHERE user_id = ?) < ?`,
			input.ID, input.UserID, input.Name, input.Key, input.CreatedAt, userID, maxAPIKeysPerUser)
		if err != nil {
			return APIKeyInput{}, fmt.Errorf("create api key: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return APIKeyInput{}, limitError
		}
		return input, nil
	}
	return APIKeyInput{}, fmt.Errorf("could not allocate a unique API key")
}

// DeleteAPIKey removes one key, scoped to its owner so a user cannot delete
// another account's credential.
func (s *Store) DeleteAPIKey(userID, keyID string) error {
	result, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ? AND user_id = ?`, keyID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// randomAPIKey builds one gateway credential. The 32 hex characters carry 128
// bits of entropy, which is what makes the key unguessable rather than merely
// long.
func randomAPIKey() (string, error) {
	secret, err := util.RandomHex(32)
	if err != nil {
		return "", err
	}
	return "sk-" + secret, nil
}

func randomKeyID() (string, error) {
	suffix, err := util.RandomHex(8)
	if err != nil {
		return "", err
	}
	return "key_" + suffix, nil
}
