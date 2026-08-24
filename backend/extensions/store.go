package extensions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"kanban/backend/crypto"
	"kanban/backend/store"
)

// encryptConfigValues encrypts sensitive config values based on the manifest's config_schema.
// Only string values for properties marked as sensitive are encrypted.
// Non-string values are converted to string for encryption.
// Returns a new map with encrypted values.
func encryptConfigValues(config map[string]any, schema *ConfigSchema) map[string]any {
	if schema == nil || len(schema.Properties) == 0 || config == nil {
		return config
	}

	result := make(map[string]any, len(config))
	for k, v := range config {
		prop := schema.GetProperty(k)
		if prop == nil || !prop.Sensitive {
			result[k] = v
			continue
		}
		// Convert value to string for encryption
		strVal := fmt.Sprintf("%v", v)
		encrypted, err := crypto.Encrypt(strVal)
		if err != nil {
			// If encryption fails, store as-is (graceful degradation)
			result[k] = v
		} else {
			result[k] = encrypted
		}
	}
	return result
}

// decryptConfigValues decrypts sensitive config values based on the manifest's config_schema.
// Returns a new map with decrypted values.
func decryptConfigValues(config map[string]any, schema *ConfigSchema) map[string]any {
	if schema == nil || len(schema.Properties) == 0 || config == nil {
		return config
	}

	result := make(map[string]any, len(config))
	for k, v := range config {
		prop := schema.GetProperty(k)
		if prop == nil || !prop.Sensitive {
			result[k] = v
			continue
		}
		strVal, okStr := v.(string)
		if !okStr || strVal == "" {
			result[k] = v
			continue
		}
		decrypted, err := crypto.Decrypt(strVal)
		if err != nil {
			// If decryption fails, leave as-is (may be plain text from before encryption)
			result[k] = v
		} else {
			result[k] = decrypted
		}
	}
	return result
}

// ExtensionStore provides access to the extensions table.
type ExtensionStore struct {
	db *store.Store
}

// NewExtensionStore creates a new ExtensionStore.
func NewExtensionStore(s *store.Store) *ExtensionStore {
	return &ExtensionStore{db: s}
}

// InitSchema creates the extensions table if it doesn't exist.
func (es *ExtensionStore) InitSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS extensions (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    author            TEXT NOT NULL DEFAULT '',
    version           TEXT NOT NULL,
    type              TEXT NOT NULL,
    manifest          TEXT NOT NULL,
    config            TEXT NOT NULL DEFAULT '{}',
    container_config  TEXT NOT NULL DEFAULT '{}',
    sandbox_config    TEXT NOT NULL DEFAULT '{}',
    install_source    TEXT NOT NULL DEFAULT 'manual',
    image_ref         TEXT DEFAULT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    installed_at      TEXT NOT NULL,
    built_at          TEXT NOT NULL DEFAULT '',
    repo              TEXT DEFAULT '',
    commit_sha        TEXT DEFAULT '',
    subdir            TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS extension_storage (
    ext_id     TEXT NOT NULL,
    namespace  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (ext_id, namespace, key)
);
`
	_, err := es.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrate: rename updated_at → built_at for existing tables
	_, _ = es.db.Exec(`ALTER TABLE extensions RENAME COLUMN updated_at TO built_at`)

	// Migrate: add git source columns (no-op if already present)
	for _, stmt := range []string{
		`ALTER TABLE extensions ADD COLUMN repo TEXT DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN commit_sha TEXT DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN subdir TEXT DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN author TEXT DEFAULT ''`,
	} {
		_, _ = es.db.Exec(stmt)
	}

	_, _ = es.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_extensions_author_name ON extensions (author, name)`)

	return nil
}

// List returns all extensions ordered by name.
func (es *ExtensionStore) List() ([]Extension, error) {
	return es.GetByType("")
}

// GetByType returns extensions filtered by type (empty = all).
func (es *ExtensionStore) GetByType(extType string) ([]Extension, error) {
	var query string
	var args []any
	if extType != "" {
		query = `SELECT id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir FROM extensions WHERE type = ? ORDER BY name`
		args = append(args, extType)
	} else {
		query = `SELECT id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir FROM extensions ORDER BY name`
	}

	rows, err := es.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query extensions: %w", err)
	}
	defer rows.Close()

	var extensions []Extension
	for rows.Next() {
		ext, err := scanExtension(rows)
		if err != nil {
			return nil, fmt.Errorf("scan extension: %w", err)
		}
		extensions = append(extensions, *ext)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate extensions: %w", err)
	}
	return extensions, nil
}

// GetByID retrieves an extension by ID.
func (es *ExtensionStore) GetByID(id string) (*Extension, error) {
	return es.getBy("id", id)
}

// GetByName retrieves an extension by name (records have timestamped IDs).
func (es *ExtensionStore) GetByName(name string) (*Extension, error) {
	return es.getBy("name", name)
}

func (es *ExtensionStore) getBy(column, value string) (*Extension, error) {
	var ext Extension
	var manifestJSON, configJSON, containerConfigJSON, sandboxConfigJSON, imageRef sql.NullString
	var extType, installSource string
	var enabled int
	var installedAt, builtAt string
	var repo, commitSHA, subdir string

	err := es.db.QueryRow(
		`SELECT id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir FROM extensions WHERE `+column+` = ?`,
		value,
	).Scan(&ext.ID, &ext.Name, &ext.Author, &ext.Version, &extType, &manifestJSON, &configJSON, &containerConfigJSON, &sandboxConfigJSON, &installSource, &imageRef, &enabled, &installedAt, &builtAt, &repo, &commitSHA, &subdir)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("extension not found: %s", value)
		}
		return nil, fmt.Errorf("query extension: %w", err)
	}

	ext.Type = extType
	ext.InstallSource = installSource
	ext.ImageRef = imageRef.String
	ext.Enabled = store.I2B(enabled)
	ext.InstalledAt = installedAt
	ext.BuiltAt = builtAt
	ext.Repo = repo
	ext.Commit = commitSHA
	ext.Subdir = subdir

	if manifestJSON.Valid {
		if err := json.Unmarshal([]byte(manifestJSON.String), &ext.Manifest); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}
	}
	if configJSON.Valid {
		if err := json.Unmarshal([]byte(configJSON.String), &ext.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	} else {
		ext.Config = map[string]any{}
	}
	ext.Config = decryptConfigValues(ext.Config, ext.Manifest.ConfigSchema)
	if containerConfigJSON.Valid {
		if err := json.Unmarshal([]byte(containerConfigJSON.String), &ext.ContainerConfig); err != nil {
			return nil, fmt.Errorf("unmarshal container config: %w", err)
		}
	}
	if sandboxConfigJSON.Valid {
		if err := json.Unmarshal([]byte(sandboxConfigJSON.String), &ext.SandboxConfig); err != nil {
			return nil, fmt.Errorf("unmarshal sandbox config: %w", err)
		}
	}

	return &ext, nil
}

// GetByAuthorName retrieves an extension by its author/name identity key.
func (es *ExtensionStore) GetByAuthorName(author, name string) (*Extension, error) {
	var ext Extension
	var manifestJSON, configJSON, containerConfigJSON, sandboxConfigJSON, imageRef sql.NullString
	var extType, installSource string
	var enabled int
	var installedAt, builtAt string
	var repo, commitSHA, subdir string

	err := es.db.QueryRow(
		`SELECT id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir FROM extensions WHERE author = ? AND name = ?`,
		author, name,
	).Scan(&ext.ID, &ext.Name, &ext.Author, &ext.Version, &extType, &manifestJSON, &configJSON, &containerConfigJSON, &sandboxConfigJSON, &installSource, &imageRef, &enabled, &installedAt, &builtAt, &repo, &commitSHA, &subdir)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("extension not found: %s/%s", author, name)
		}
		return nil, fmt.Errorf("query extension: %w", err)
	}

	ext.Type = extType
	ext.InstallSource = installSource
	ext.ImageRef = imageRef.String
	ext.Enabled = store.I2B(enabled)
	ext.InstalledAt = installedAt
	ext.BuiltAt = builtAt
	ext.Repo = repo
	ext.Commit = commitSHA
	ext.Subdir = subdir

	if manifestJSON.Valid {
		if err := json.Unmarshal([]byte(manifestJSON.String), &ext.Manifest); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}
	}
	if configJSON.Valid {
		if err := json.Unmarshal([]byte(configJSON.String), &ext.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	} else {
		ext.Config = map[string]any{}
	}
	// Decrypt sensitive config values after loading
	ext.Config = decryptConfigValues(ext.Config, ext.Manifest.ConfigSchema)
	if containerConfigJSON.Valid {
		if err := json.Unmarshal([]byte(containerConfigJSON.String), &ext.ContainerConfig); err != nil {
			return nil, fmt.Errorf("unmarshal container config: %w", err)
		}
	}
	if sandboxConfigJSON.Valid {
		if err := json.Unmarshal([]byte(sandboxConfigJSON.String), &ext.SandboxConfig); err != nil {
			return nil, fmt.Errorf("unmarshal sandbox config: %w", err)
		}
	}

	return &ext, nil
}

// Create inserts a new extension.
func (es *ExtensionStore) Create(ext *Extension) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if ext.InstalledAt == "" {
		ext.InstalledAt = now
	}
	if ext.BuiltAt == "" {
		ext.BuiltAt = now
	}

	manifestJSON, err := json.Marshal(ext.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	// Encrypt sensitive config values before storage
	configToStore := encryptConfigValues(ext.Config, ext.Manifest.ConfigSchema)
	configJSON, err := json.Marshal(configToStore)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	containerConfigJSON, err := json.Marshal(ext.ContainerConfig)
	if err != nil {
		return fmt.Errorf("marshal container config: %w", err)
	}
	if containerConfigJSON == nil {
		containerConfigJSON = []byte("{}")
	}
	sandboxConfigJSON, err := json.Marshal(ext.SandboxConfig)
	if err != nil {
		return fmt.Errorf("marshal sandbox config: %w", err)
	}
	if sandboxConfigJSON == nil {
		sandboxConfigJSON = []byte("{}")
	}

	_, err = es.db.Exec(
		`INSERT INTO extensions (id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ext.ID, ext.Name, ext.Author, ext.Version, ext.Type, string(manifestJSON), string(configJSON), string(containerConfigJSON), string(sandboxConfigJSON), ext.InstallSource, ext.ImageRef, store.B2I(ext.Enabled), ext.InstalledAt, ext.BuiltAt, ext.Repo, ext.Commit, ext.Subdir,
	)
	if err != nil {
		return fmt.Errorf("insert extension: %w", err)
	}
	return nil
}

// Update updates an existing extension.
func (es *ExtensionStore) Update(id string, ext *Extension) error {
	manifestJSON, err := json.Marshal(ext.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	// Encrypt sensitive config values before storage
	configToStore := encryptConfigValues(ext.Config, ext.Manifest.ConfigSchema)
	configJSON, err := json.Marshal(configToStore)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	containerConfigJSON, err := json.Marshal(ext.ContainerConfig)
	if err != nil {
		return fmt.Errorf("marshal container config: %w", err)
	}
	if containerConfigJSON == nil {
		containerConfigJSON = []byte("{}")
	}
	sandboxConfigJSON, err := json.Marshal(ext.SandboxConfig)
	if err != nil {
		return fmt.Errorf("marshal sandbox config: %w", err)
	}
	if sandboxConfigJSON == nil {
		sandboxConfigJSON = []byte("{}")
	}

	result, err := es.db.Exec(
		`UPDATE extensions SET version = ?, manifest = ?, config = ?, container_config = ?, sandbox_config = ?, image_ref = ?, repo = ?, commit_sha = ?, subdir = ? WHERE id = ?`,
		ext.Version, string(manifestJSON), string(configJSON), string(containerConfigJSON), string(sandboxConfigJSON), ext.ImageRef, ext.Repo, ext.Commit, ext.Subdir, id,
	)
	if err != nil {
		return fmt.Errorf("update extension: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("extension not found: %s", id)
	}
	return nil
}

// Delete removes an extension.
func (es *ExtensionStore) Delete(id string) error {
	result, err := es.db.Exec(`DELETE FROM extensions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete extension: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("extension not found: %s", id)
	}
	return nil
}

// SetEnabled toggles the enabled state of an extension.
func (es *ExtensionStore) SetEnabled(id string, enabled bool) error {
	result, err := es.db.Exec(
		`UPDATE extensions SET enabled = ? WHERE id = ?`,
		store.B2I(enabled), id,
	)
	if err != nil {
		return fmt.Errorf("set enabled: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("extension not found: %s", id)
	}
	return nil
}

// SetBuiltAt updates the built_at timestamp for an extension.
func (es *ExtensionStore) SetBuiltAt(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := es.db.Exec(
		`UPDATE extensions SET built_at = ? WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("set built_at: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("extension not found: %s", id)
	}
	return nil
}

// StorageGet returns the value for a single key in an extension's namespace.
// Returns (nil, nil) if the key does not exist.
func (es *ExtensionStore) StorageGet(extID, namespace, key string) (*KVEntry, error) {
	var value, updatedAt string
	err := es.db.QueryRow(
		`SELECT value, updated_at FROM extension_storage WHERE ext_id = ? AND namespace = ? AND key = ?`,
		extID, namespace, key,
	).Scan(&value, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query storage: %w", err)
	}
	return &KVEntry{Key: key, Value: json.RawMessage(value), UpdatedAt: updatedAt}, nil
}

// StorageList returns all keys in a namespace, optionally filtered by prefix.
func (es *ExtensionStore) StorageList(extID, namespace, prefix string) ([]KVEntry, error) {
	query := `SELECT key, value, updated_at FROM extension_storage WHERE ext_id = ? AND namespace = ?`
	args := []any{extID, namespace}
	if prefix != "" {
		query += ` AND key LIKE ?`
		args = append(args, prefix+"%")
	}
	query += ` ORDER BY key`

	rows, err := es.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query storage list: %w", err)
	}
	defer rows.Close()

	var entries []KVEntry
	for rows.Next() {
		var e KVEntry
		var value string
		if err := rows.Scan(&e.Key, &value, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan storage: %w", err)
		}
		e.Value = json.RawMessage(value)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage: %w", err)
	}
	return entries, nil
}

// StorageSet writes (or overwrites) a key in an extension's namespace.
// The value must be valid JSON. Returns the stored entry.
func (es *ExtensionStore) StorageSet(extID, namespace, key string, value json.RawMessage) (*KVEntry, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, fmt.Errorf("value must be valid JSON")
	}
	if len(value) > maxExtensionValueBytes {
		return nil, fmt.Errorf("value exceeds %d bytes", maxExtensionValueBytes)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := es.db.Exec(
		`INSERT INTO extension_storage (ext_id, namespace, key, value, updated_at) VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(ext_id, namespace, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		extID, namespace, key, string(value), now,
	)
	if err != nil {
		return nil, fmt.Errorf("set storage: %w", err)
	}
	return &KVEntry{Key: key, Value: value, UpdatedAt: now}, nil
}

// StorageDelete removes a key from an extension's namespace. No-op if missing.
func (es *ExtensionStore) StorageDelete(extID, namespace, key string) error {
	_, err := es.db.Exec(
		`DELETE FROM extension_storage WHERE ext_id = ? AND namespace = ? AND key = ?`,
		extID, namespace, key,
	)
	if err != nil {
		return fmt.Errorf("delete storage: %w", err)
	}
	return nil
}

// StorageDeleteAll removes ALL storage for an extension. Used when the
// extension is deleted so its scoped data is not orphaned in the database.
func (es *ExtensionStore) StorageDeleteAll(extID string) error {
	_, err := es.db.Exec(
		`DELETE FROM extension_storage WHERE ext_id = ?`,
		extID,
	)
	if err != nil {
		return fmt.Errorf("delete all storage: %w", err)
	}
	return nil
}

// scanExtension scans a row into an Extension.
func scanExtension(s scanner) (*Extension, error) {
	var ext Extension
	var manifestJSON, configJSON, containerConfigJSON, sandboxConfigJSON, imageRef sql.NullString
	var extType, installSource string
	var enabled int
	var installedAt, builtAt string
	var repo, commitSHA, subdir string

	err := s.Scan(&ext.ID, &ext.Name, &ext.Author, &ext.Version, &extType, &manifestJSON, &configJSON, &containerConfigJSON, &sandboxConfigJSON, &installSource, &imageRef, &enabled, &installedAt, &builtAt, &repo, &commitSHA, &subdir)
	if err != nil {
		return nil, err
	}

	ext.Type = extType
	ext.InstallSource = installSource
	ext.ImageRef = imageRef.String
	ext.Enabled = store.I2B(enabled)
	ext.InstalledAt = installedAt
	ext.BuiltAt = builtAt
	ext.Repo = repo
	ext.Commit = commitSHA
	ext.Subdir = subdir

	if manifestJSON.Valid {
		if err := json.Unmarshal([]byte(manifestJSON.String), &ext.Manifest); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}
	}
	if configJSON.Valid {
		if err := json.Unmarshal([]byte(configJSON.String), &ext.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	} else {
		ext.Config = map[string]any{}
	}
	// Decrypt sensitive config values after loading
	ext.Config = decryptConfigValues(ext.Config, ext.Manifest.ConfigSchema)
	if containerConfigJSON.Valid {
		if err := json.Unmarshal([]byte(containerConfigJSON.String), &ext.ContainerConfig); err != nil {
			return nil, fmt.Errorf("unmarshal container config: %w", err)
		}
	}
	if sandboxConfigJSON.Valid {
		if err := json.Unmarshal([]byte(sandboxConfigJSON.String), &ext.SandboxConfig); err != nil {
			return nil, fmt.Errorf("unmarshal sandbox config: %w", err)
		}
	}

	return &ext, nil
}

// scanner is the common interface for sql.Row and sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}
