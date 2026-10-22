package extensions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mike/backend/crypto"
	"mike/backend/errs"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
)

// mapSensitiveConfig transforms config values declared sensitive by the schema.
// Result is always a copy to avoid mutating shared maps.
func mapSensitiveConfig(config map[string]any, schema *ConfigSchema, transform func(key string, value any) (any, bool)) map[string]any {
	if schema == nil || len(schema.Properties) == 0 || config == nil {
		return config
	}

	result := make(map[string]any, len(config))
	for k, v := range config {
		if !schema.IsSensitive(k) {
			result[k] = v
			continue
		}
		if mapped, ok := transform(k, v); ok {
			result[k] = mapped
			continue
		}
		result[k] = v
	}
	return result
}

// extensionKeyPurpose is the sealing purpose for sensitive config values; prevents a value
// from being moved between extensions or keys.
const extensionKeyPurpose = "extension-config"

// encryptConfigValues encrypts sensitive config values declared in the manifest.
// Secrets are dropped if no encryption box is available.
func encryptConfigValues(box *crypto.Box, extID string, config map[string]any, schema *ConfigSchema) map[string]any {
	return mapSensitiveConfig(config, schema, func(key string, v any) (any, bool) {
		plain := fmt.Sprintf("%v", v)
		if plain == "" {
			return nil, false
		}
		if box == nil {
			logging.For(logging.Extensions).Warnf("dropping sensitive config %q of %s: no encryption key configured", key, extID)
			return "", true
		}
		record := crypto.Record(extID, key)
		if box.Opens(extensionKeyPurpose, record, plain) {
			return nil, false // this key's record already
		}
		encrypted, err := box.Encrypt(extensionKeyPurpose, record, plain)
		if err != nil {
			logging.For(logging.Extensions).Warnf("could not encrypt sensitive config %q of %s: %s", key, extID, err)
			return "", true
		}
		return encrypted, true
	})
}

// decryptConfigValues decrypts sensitive values declared in the manifest.
// Non-records pass through unchanged.
func decryptConfigValues(box *crypto.Box, extID string, config map[string]any, schema *ConfigSchema) map[string]any {
	return mapSensitiveConfig(config, schema, func(key string, v any) (any, bool) {
		strVal, okStr := v.(string)
		if !okStr || strVal == "" {
			return nil, false
		}
		if box == nil {
			return "", true
		}
		decrypted, err := box.Decrypt(extensionKeyPurpose, crypto.Record(extID, key), strVal)
		if errors.Is(err, crypto.ErrNotRecord) {
			return nil, false
		}
		if err != nil {
			logging.For(logging.Extensions).Warnf("sensitive config %q of %s did not open: %s", key, extID, err)
			return "", true
		}
		return decrypted, true
	})
}

// rawSQL is the database interface for extensions. Uses raw SQL because the extensions
// table is managed separately from the core store.
type rawSQL interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// projectPaths is all this package needs from the project table: the workspace paths an
// extension sandbox may mount.
type projectPaths interface {
	ListProjects() ([]types.Project, error)
}

// ExtensionStore provides access to the extensions table.
type ExtensionStore struct {
	db       rawSQL
	projects projectPaths
	box      *crypto.Box // seals the values a manifest marks sensitive; nil drops a secret
}

// NewExtensionStore creates a new ExtensionStore.
func NewExtensionStore(s *store.Store, box *crypto.Box) *ExtensionStore {
	return &ExtensionStore{db: s.RawSQL(), projects: s, box: box}
}

var (
	_ rawSQL       = (*store.Store)(nil)
	_ projectPaths = (*store.Store)(nil)
)

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

	// These ALTERs are repeatable: a statement already applied errors, and that error is expected.
	_, _ = es.db.Exec(`ALTER TABLE extensions RENAME COLUMN updated_at TO built_at`)

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

// allProjects accesses project table for mount paths. Returns nil if project accessor is missing.
func (es *ExtensionStore) allProjects() ([]types.Project, error) {
	if es == nil || es.projects == nil {
		return nil, nil
	}
	return es.projects.ListProjects()
}

// ListProjectPaths returns host paths of registered projects to form the
// extension-sandbox mount allowlist.
func (es *ExtensionStore) ListProjectPaths() ([]string, error) {
	projects, err := es.allProjects()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.Path != "" {
			paths = append(paths, p.Path)
		}
	}
	return paths, nil
}

// ListProjectSandboxMountPaths returns the host paths declared in project Sandbox Configurations.
func (es *ExtensionStore) ListProjectSandboxMountPaths() ([]string, error) {
	projects, err := es.allProjects()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range projects {
		for _, m := range p.SandboxMounts {
			if host := strings.TrimSpace(m.HostPath); host != "" {
				paths = append(paths, host)
			}
		}
	}
	return paths, nil
}

// extensionColumns is the SELECT list every extensions query shares; the order must match
// scanExtension's argument order.
const extensionColumns = `id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir`

// GetByType returns extensions filtered by type (empty = all).
func (es *ExtensionStore) GetByType(extType string) ([]Extension, error) {
	var query string
	var args []any
	if extType != "" {
		query = `SELECT ` + extensionColumns + ` FROM extensions WHERE type = ? ORDER BY name`
		args = append(args, extType)
	} else {
		query = `SELECT ` + extensionColumns + ` FROM extensions ORDER BY name`
	}

	rows, err := es.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query extensions: %w", err)
	}
	defer rows.Close()

	var extensions []Extension
	for rows.Next() {
		ext, err := es.scanExtension(rows)
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

// ErrExtensionNotFound is an extension-specific instance of errs.ErrNotFound.
var ErrExtensionNotFound = fmt.Errorf("extension %w", errs.ErrNotFound)

// GetByID retrieves an extension by ID.
func (es *ExtensionStore) GetByID(id string) (*Extension, error) {
	ext, err := es.queryOne(`WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", ErrExtensionNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return ext, nil
}

// GetByAuthorName retrieves extension by author/name; this pair is the stable
// identity across timestamped IDs.
func (es *ExtensionStore) GetByAuthorName(author, name string) (*Extension, error) {
	ext, err := es.queryOne(`WHERE author = ? AND name = ?`, author, name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s/%s", ErrExtensionNotFound, author, name)
	}
	if err != nil {
		return nil, err
	}
	return ext, nil
}

// queryOne runs single-row query. WHERE clause is a literal to prevent injection;
// returns unwrapped sql.ErrNoRows.
func (es *ExtensionStore) queryOne(where string, args ...any) (*Extension, error) {
	ext, err := es.scanExtension(es.db.QueryRow(
		`SELECT `+extensionColumns+` FROM extensions `+where, args...))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query extension: %w", err)
	}
	if err == sql.ErrNoRows {
		return nil, err
	}
	return ext, nil
}

// extensionRowJSON is one extension row's four JSON columns, ready for the INSERT/UPDATE
// parameter list.
type extensionRowJSON struct {
	manifest  string
	config    string
	container string
	sandbox   string
}

// rowJSON renders those columns, encrypting sensitive config values against this extension's schema.
func (es *ExtensionStore) rowJSON(id string, ext *Extension) (extensionRowJSON, error) {
	manifestJSON, err := json.Marshal(ext.Manifest)
	if err != nil {
		return extensionRowJSON{}, fmt.Errorf("marshal manifest: %w", err)
	}
	configToStore := encryptConfigValues(es.box, id, ext.Config, ext.Manifest.ConfigSchema)
	configJSON, err := json.Marshal(configToStore)
	if err != nil {
		return extensionRowJSON{}, fmt.Errorf("marshal config: %w", err)
	}
	containerJSON, err := json.Marshal(ext.ContainerConfig)
	if err != nil {
		return extensionRowJSON{}, fmt.Errorf("marshal container config: %w", err)
	}
	sandboxJSON, err := json.Marshal(ext.SandboxConfig)
	if err != nil {
		return extensionRowJSON{}, fmt.Errorf("marshal sandbox config: %w", err)
	}
	return extensionRowJSON{
		manifest:  string(manifestJSON),
		config:    string(configJSON),
		container: string(containerJSON),
		sandbox:   string(sandboxJSON),
	}, nil
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

	columns, err := es.rowJSON(ext.ID, ext)
	if err != nil {
		return err
	}

	_, err = es.db.Exec(
		`INSERT INTO extensions (id, name, author, version, type, manifest, config, container_config, sandbox_config, install_source, image_ref, enabled, installed_at, built_at, repo, commit_sha, subdir) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ext.ID, ext.Name, ext.Author, ext.Version, ext.Type, columns.manifest, columns.config, columns.container, columns.sandbox, ext.InstallSource, ext.ImageRef, store.B2I(ext.Enabled), ext.InstalledAt, ext.BuiltAt, ext.Repo, ext.Commit, ext.Subdir,
	)
	if err != nil {
		return fmt.Errorf("insert extension: %w", err)
	}
	return nil
}

// execOne runs statement for a specific extension; returns error if no rows were affected.
func (es *ExtensionStore) execOne(op, id, query string, args ...any) error {
	result, err := es.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("extension not found: %s", id)
	}
	return nil
}

// Update updates an existing extension.
func (es *ExtensionStore) Update(id string, ext *Extension) error {
	columns, err := es.rowJSON(id, ext)
	if err != nil {
		return err
	}

	return es.execOne("update extension", id,
		`UPDATE extensions SET version = ?, manifest = ?, config = ?, container_config = ?, sandbox_config = ?, image_ref = ?, repo = ?, commit_sha = ?, subdir = ? WHERE id = ?`,
		ext.Version, columns.manifest, columns.config, columns.container, columns.sandbox, ext.ImageRef, ext.Repo, ext.Commit, ext.Subdir, id,
	)
}

// Delete removes an extension.
func (es *ExtensionStore) Delete(id string) error {
	return es.execOne("delete extension", id, `DELETE FROM extensions WHERE id = ?`, id)
}

// SetEnabled toggles the enabled state of an extension.
func (es *ExtensionStore) SetEnabled(id string, enabled bool) error {
	return es.execOne("set enabled", id, `UPDATE extensions SET enabled = ? WHERE id = ?`, store.B2I(enabled), id)
}

// SetImageRef records the sandbox image in both image_ref and container_config.image.
func (es *ExtensionStore) SetImageRef(id, imageRef string, containerConfig ContainerConfig) error {
	containerConfigJSON, err := json.Marshal(containerConfig)
	if err != nil {
		return fmt.Errorf("marshal container config: %w", err)
	}
	if containerConfigJSON == nil {
		containerConfigJSON = []byte("{}")
	}
	return es.execOne("set image_ref", id, `UPDATE extensions SET image_ref = ?, container_config = ? WHERE id = ?`,
		imageRef, string(containerConfigJSON), id)
}

// SetBuiltAt updates the built_at timestamp for an extension.
func (es *ExtensionStore) SetBuiltAt(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return es.execOne("set built_at", id, `UPDATE extensions SET built_at = ? WHERE id = ?`, now, id)
}

// StorageGet returns the value for one key in an extension's namespace, or (nil, nil) when the key does
// not exist.
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

// likeEscaper quotes a literal for a LIKE pattern, where '%' and '_' are wildcards.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// StorageList returns all keys in a namespace, optionally filtered by prefix.
func (es *ExtensionStore) StorageList(extID, namespace, prefix string) ([]KVEntry, error) {
	query := `SELECT key, value, updated_at FROM extension_storage WHERE ext_id = ? AND namespace = ?`
	args := []any{extID, namespace}
	if prefix != "" {
		// The prefix is data, not a pattern. Its wildcards must not widen the listing.
		query += ` AND key LIKE ? ESCAPE '\'`
		args = append(args, likeEscaper.Replace(prefix)+"%")
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

// StorageSet writes a key in an extension's namespace; the value must be valid JSON. It returns the
// stored entry.
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

// StorageDelete removes a key from an extension's namespace; a missing key is not an error.
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

// StorageDeleteAll removes all stored values for an extension.
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

func (es *ExtensionStore) scanExtension(s scanner) (*Extension, error) {
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
	ext.Config = decryptConfigValues(es.box, ext.ID, ext.Config, ext.Manifest.ConfigSchema)
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
