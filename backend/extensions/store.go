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

// mapSensitiveConfig applies transform to every config value the schema declares sensitive and
// copies the rest unchanged. Encrypting, decrypting and masking share it, including the guard:
// with no schema nothing is known to be secret, so nothing is touched — guessing by key name
// would encrypt a non-secret. A transform returns ok=false to leave a value alone.
//
// The result is always a copy: these maps reach both the store and the browser, and mutating the
// input could leak a decrypted secret into a cached response.
func mapSensitiveConfig(config map[string]any, schema *ConfigSchema, transform func(key string, value any) (any, bool)) map[string]any {
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
		if mapped, ok := transform(k, v); ok {
			result[k] = mapped
			continue
		}
		result[k] = v
	}
	return result
}

// extensionKeyPurpose is the record kind an extension's sensitive config value is sealed under, next to
// the extension id and the config key, so a value cannot move between them.
const extensionKeyPurpose = "extension-config"

// encryptConfigValues encrypts the sensitive config values the manifest declares. With no box there is no
// key to seal with, so a secret is dropped rather than written plain.
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
			return nil, false // already this key's record
		}
		encrypted, err := box.Encrypt(extensionKeyPurpose, record, plain)
		if err != nil {
			logging.For(logging.Extensions).Warnf("could not encrypt sensitive config %q of %s: %s", key, extID, err)
			return "", true
		}
		return encrypted, true
	})
}

// decryptConfigValues decrypts sensitive config values based on the manifest's config_schema.
//
// A record that will not open answers as no value; a value that could not be a record at all passes
// through unchanged.
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

// rawSQL is this package's whole database need. The extensions table is its schema, not the
// store's, so it speaks SQL - but it must not inherit the store's typed accessors with it.
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
	// box seals the config values a manifest marks sensitive. Nil means a secret is dropped rather than
	// stored in the clear.
	box *crypto.Box
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

// ListProjectPaths returns the host path of every registered project. These paths
// are part of the extension-sandbox mount allowlist (see MountPolicy): an
// extension may mount what the app already mounts for its workspaces, nothing more.
func (es *ExtensionStore) ListProjectPaths() ([]string, error) {
	if es == nil || es.projects == nil {
		return nil, nil
	}
	projects, err := es.projects.ListProjects()
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

// ListProjectSandboxMountPaths returns the host paths every project declares in its Sandbox
// Configuration. The task sandbox already bind-mounts them, so the LSP sandbox may borrow them too,
// but read-only: see MountPolicy.ForceReadOnly.
func (es *ExtensionStore) ListProjectSandboxMountPaths() ([]string, error) {
	if es == nil || es.projects == nil {
		return nil, nil
	}
	projects, err := es.projects.ListProjects()
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

// extensionColumns is the SELECT list shared by every extensions query; the
// order must match scanExtension's argument order.
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

// ErrExtensionNotFound is the extensions member of the errs.ErrNotFound family. GetByID
// wraps it with the ID in the message, because callers display that.
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

// GetByAuthorName retrieves an extension by its author/name identity key — the
// pair is the extension's stable identity (IDs are timestamped, so the same
// tool installed twice is one identity).
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

// queryOne runs a single-row extensions query and decodes it. The WHERE clause
// is a literal fragment (never built from caller data); values are always bound
// as parameters. sql.ErrNoRows is returned unwrapped so the caller can name the
// identity it looked up.
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
	configToStore := encryptConfigValues(es.box, ext.ID, ext.Config, ext.Manifest.ConfigSchema)
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
	configToStore := encryptConfigValues(es.box, id, ext.Config, ext.Manifest.ConfigSchema)
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

// SetImageRef records the image an extension's sandbox runs, in both places it is kept: image_ref (the
// UI and the delete path) and container_config.image (the container create). Only those two columns -
// the caller's copy of the manifest or config may predate another writer.
func (es *ExtensionStore) SetImageRef(id, imageRef string, containerConfig ContainerConfig) error {
	containerConfigJSON, err := json.Marshal(containerConfig)
	if err != nil {
		return fmt.Errorf("marshal container config: %w", err)
	}
	if containerConfigJSON == nil {
		containerConfigJSON = []byte("{}")
	}
	result, err := es.db.Exec(
		`UPDATE extensions SET image_ref = ?, container_config = ? WHERE id = ?`,
		imageRef, string(containerConfigJSON), id,
	)
	if err != nil {
		return fmt.Errorf("set image_ref: %w", err)
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
		// A manifest stored before file types existed is normalized here, so every reader sees one
		// shape and none carries a legacy branch; idempotent, so no migration.
		ext.Manifest.Capabilities.GetLSP().NormalizeFileTypes()
	}
	if configJSON.Valid {
		if err := json.Unmarshal([]byte(configJSON.String), &ext.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	} else {
		ext.Config = map[string]any{}
	}
	// Decrypt sensitive config values after loading
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
