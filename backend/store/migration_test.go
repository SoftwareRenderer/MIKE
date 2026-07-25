package store

import (
	"strings"
	"testing"
)

func TestMigratePiAgentToAgentURL(t *testing.T) {
	s := testStore(t)

	// Clear the migration marker (set by NewStore) so we can test the migration
	s.Exec(`DELETE FROM config WHERE key = 'migration_pi_to_agent'`)

	// Insert a provider with pi://agent URL (models field required by Provider.UnmarshalJSON)
	providersJSON := `[{"name":"pi","kind":"agent","url":"pi://agent","concurrency":1,"models":[]},{"name":"openai","kind":"llm","url":"https://api.openai.com","concurrency":3,"models":["gpt-4"]}]`
	s.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES ('providers', ?)`, providersJSON)

	// Run migration
	err := s.migratePiAgentToAgentURL()
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify providers updated
	var providersRaw string
	s.QueryRow(`SELECT value FROM config WHERE key = 'providers'`).Scan(&providersRaw)
	if strings.Contains(providersRaw, `"url":"agent://pi"`) {
		// Good — pi provider URL was updated
	} else {
		t.Errorf("pi provider URL not updated: %s", providersRaw)
	}
	if !strings.Contains(providersRaw, `"url":"https://api.openai.com"`) {
		t.Errorf("openai provider changed unexpectedly: %s", providersRaw)
	}

	// Verify migration marker set
	var marker int
	s.QueryRow(`SELECT COUNT(*) FROM config WHERE key = 'migration_pi_to_agent'`).Scan(&marker)
	if marker == 0 {
		t.Error("migration marker not set")
	}

	// Run again — should be idempotent (no-op)
	// Reset providers to pi://agent to verify migration doesn't re-run
	s.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES ('providers', ?)`, providersJSON)
	err = s.migratePiAgentToAgentURL()
	if err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
	s.QueryRow(`SELECT value FROM config WHERE key = 'providers'`).Scan(&providersRaw)
	// Should still have pi://agent since migration was idempotent (marker already set)
	if strings.Contains(providersRaw, `"url":"agent://pi"`) {
		t.Error("idempotent migration should not re-run")
	}
}
