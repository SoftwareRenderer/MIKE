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
	if schema == nil || schema.Properties == nil || config == nil {
		return config
	}

	result := make(map[string]any, len(config))
	for k, v := range config {
		prop, ok := schema.Properties[k]
		if !ok || !prop.Sensitive {
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
	if schema == nil || schema.Properties == nil || config == nil {
		return config
	}

	result := make(map[string]any, len(config))
	for k, v := range config {
		prop, ok := schema.Properties[k]
		if !ok || !prop.Sensitive {
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
    built_at          TEXT NOT NULL DEFAULT ''
);
`
	_, err := es.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrate: rename updated_at → built_at for existing tables
	_, _ = es.db.Exec(`ALTER TABLE extensions RENAME COLUMN updated_at TO built_at`)

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
		query = `SELECT id, name, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at FROM extensions WHERE type = ? ORDER BY name`
		args = append(args, extType)
	} else {
		query = `SELECT id, name, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at FROM extensions ORDER BY name`
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
	var ext Extension
	var manifestJSON, configJSON, containerConfigJSON, sandboxConfigJSON, imageRef sql.NullString
	var extType, installSource string
	var enabled int
	var installedAt, builtAt string

	err := es.db.QueryRow(
		`SELECT id, name, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at FROM extensions WHERE id = ?`,
		id,
	).Scan(&ext.ID, &ext.Name, &ext.Version, &extType, &manifestJSON, &configJSON, &containerConfigJSON, &sandboxConfigJSON, &installSource, &imageRef, &enabled, &installedAt, &builtAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("extension not found: %s", id)
		}
		return nil, fmt.Errorf("query extension: %w", err)
	}

	ext.Type = extType
	ext.InstallSource = installSource
	ext.ImageRef = imageRef.String
	ext.Enabled = i2b(enabled)
	ext.InstalledAt = installedAt
	ext.BuiltAt = builtAt

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
		`INSERT INTO extensions (id, name, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ext.ID, ext.Name, ext.Version, ext.Type, string(manifestJSON), string(configJSON), string(containerConfigJSON), string(sandboxConfigJSON), ext.InstallSource, ext.ImageRef, b2i(ext.Enabled), ext.InstalledAt, ext.BuiltAt,
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
		`UPDATE extensions SET version = ?, manifest = ?, config = ?, container_config = ?, sandbox_config = ?, image_ref = ? WHERE id = ?`,
		ext.Version, string(manifestJSON), string(configJSON), string(containerConfigJSON), string(sandboxConfigJSON), ext.ImageRef, id,
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
		b2i(enabled), id,
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

// scanExtension scans a row into an Extension.
func scanExtension(s scanner) (*Extension, error) {
	var ext Extension
	var manifestJSON, configJSON, containerConfigJSON, sandboxConfigJSON, imageRef sql.NullString
	var extType, installSource string
	var enabled int
	var installedAt, builtAt string

	err := s.Scan(&ext.ID, &ext.Name, &ext.Version, &extType, &manifestJSON, &configJSON, &containerConfigJSON, &sandboxConfigJSON, &installSource, &imageRef, &enabled, &installedAt, &builtAt)
	if err != nil {
		return nil, err
	}

	ext.Type = extType
	ext.InstallSource = installSource
	ext.ImageRef = imageRef.String
	ext.Enabled = i2b(enabled)
	ext.InstalledAt = installedAt
	ext.BuiltAt = builtAt

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

// b2i converts bool to int (0 or 1).
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// i2b converts int (0 or 1) to bool.
func i2b(i int) bool {
	return i != 0
}

// scanner is the common interface for sql.Row and sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}
