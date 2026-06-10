package store

import "fmt"

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
	return err
}

// ResetData clears all test data except config and projects.
// Projects are preserved so tests can find them. Used by e2e tests for a clean slate.
func (s *Store) ResetData() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear in reverse dependency order to respect foreign keys
	tables := []string{
		"task_messages",
		"home_agent_messages",
		"review_comments",
		"task_dependencies",
		"tasks",
	}
	for _, table := range tables {
		if _, err := s.db.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return nil
}
