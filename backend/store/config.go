package store

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
