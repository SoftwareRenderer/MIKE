package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// installationSaltKey holds this database's KDF salt. It is not a secret, and it must never change:
// the derived key opens every stored secret.
const installationSaltKey = "installation_salt"

// installationSaltBytes matches crypto.SaltSize.
const installationSaltBytes = 16

// InstallationSalt returns the salt the process's data key is derived from, writing a fresh one on
// first use.
func (s *Store) InstallationSalt() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var raw string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key = ?`, installationSaltKey).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		raw = ""
	case err != nil:
		return nil, fmt.Errorf("read installation salt: %w", err)
	}
	if raw != "" {
		salt, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decode installation salt: %w", err)
		}
		return salt, nil
	}

	salt := make([]byte, installationSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate installation salt: %w", err)
	}
	// OR IGNORE, not REPLACE: the first salt is the only one that can open what follows.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES (?, ?)`,
		installationSaltKey, base64.StdEncoding.EncodeToString(salt)); err != nil {
		return nil, fmt.Errorf("store installation salt: %w", err)
	}
	return salt, nil
}

// GetConfigValues reads several config rows in one pass, which beats one query per key on a store that
// runs one connection. A key with no row is absent from the map; a value stored as the empty string is
// present and empty.
func (s *Store) GetConfigValues(keys ...string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.db.Query(`SELECT key, value FROM config WHERE key IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// GetConfigValue retrieves a single config value by key.
func (s *Store) GetConfigValue(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var value string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == nil {
		return value, nil
	}
	return "", err
}

// SetConfigValue sets or updates a config value.
func (s *Store) SetConfigValue(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, key, value)
	// The write and the counter move together under s.mu, so a reader that saw the old generation
	// cannot also see the new value.
	s.configGen.Add(1)
	return err
}

// ConfigGeneration counts writes to the config table made through SetConfigValue, so a consumer that
// caches a parsed config compares this instead of re-reading. The migration stamps are the only other
// writers, and they run before any consumer exists.
func (s *Store) ConfigGeneration() uint64 { return s.configGen.Load() }
