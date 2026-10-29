package core

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultDatabasePath is used when --db is not supplied.
const DefaultDatabasePath = "llm-proxy.db"

// Column lists shared by every read of a table. The order must match the scan
// functions below.
const (
	providerFields = `id, name, base_url, api_key, max_retries, timeout, endpoints, headers, proxy, upstream_models`
	modelFields    = `id, name, routing, context_length, max_output_length, modalities, output_modalities, reasoning, tools, timeout`
	userFields     = `id, name, allowed_models, is_admin, password_hash`
	userListFields = `u.id, u.name, u.allowed_models, u.is_admin,
		(SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id)`
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrAdminExists is returned when administrator registration is attempted after
// the first administrator has already been created.
var ErrAdminExists = errors.New("an administrator account already exists")

// ErrUserIDTaken is returned when an account registration reuses an existing id.
var ErrUserIDTaken = errors.New("user id is already in use")

// Store owns the SQLite database. All configuration is loaded from here; there
// is no configuration file.
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore opens (and creates when necessary) the SQLite database.
func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultDatabasePath
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
		// The database holds provider secrets and API keys. Create a new file
		// with owner-only permissions; an existing file keeps its own mode.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		switch {
		case err == nil:
			if err := file.Close(); err != nil {
				return nil, fmt.Errorf("create database file: %w", err)
			}
		case !errors.Is(err, os.ErrExist):
			return nil, fmt.Errorf("create database file: %w", err)
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite allows a single writer. A single connection also keeps in-memory
	// databases consistent in tests.
	db.SetMaxOpenConns(1)
	// A quick sanity check that the connection opens with the DSN pragmas, so a
	// malformed path fails here instead of on the first query.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	store := &Store{db: db, path: path}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// sqliteDSN attaches the connection pragmas to the database path. They must be
// part of the DSN rather than a one-off Exec: database/sql discards a broken
// connection and opens a replacement without replaying it, which would silently
// drop foreign keys, WAL journaling and the busy timeout.
func sqliteDSN(path string) string {
	// A path that already carries a query string (or a full SQLite URI) is
	// passed through untouched; the pragmas below cannot be merged into it
	// without rewriting the caller's URI.
	if strings.Contains(path, "?") || strings.HasPrefix(path, "file:") {
		return path
	}
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(5000)")
	return path + "?" + params.Encode()
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// SetPassword replaces a user's password verifier.
func (s *Store) SetPassword(userID, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	encoded, err := hashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		encoded, time.Now().UTC().Format(time.RFC3339Nano), userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
