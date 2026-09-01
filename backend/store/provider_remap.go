package store

import "fmt"

// providerNameTables are the rows that record which provider served a call. The only table
// names ever interpolated below, and a literal rather than an argument.
var providerNameTables = []string{"task_messages", "project_token_stats", "project_token_tasks"}

// RemapProviderNames rewrites provider_name from a legacy slug to its provider ID in every
// table that records one. Provider definitions are a config blob, but these rows live here,
// so this is the only place allowed to rewrite them. One lock for the whole remap, so no
// reader sees history updated halfway.
func (s *Store) RemapProviderNames(oldSlugToID map[string]string) error {
	if len(oldSlugToID) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, table := range providerNameTables {
		for slug, id := range oldSlugToID {
			if slug == id {
				continue
			}
			if _, err := s.db.Exec(`UPDATE `+table+` SET provider_name = ? WHERE provider_name = ?`, id, slug); err != nil {
				return fmt.Errorf("remap provider %q in %s: %w", slug, table, err)
			}
		}
	}
	return nil
}
