package config

import (
	"encoding/json"
	"strings"
	"testing"

	"mike/backend/crypto"
	"mike/backend/store"
	"mike/backend/types"
)

// firstStoredKey is the provider API key exactly as the providers row holds it.
func firstStoredKey(t *testing.T, db *store.Store) string {
	t.Helper()
	raw, err := db.GetConfigValue("providers")
	if err != nil {
		t.Fatalf("read providers row: %v", err)
	}
	var providers []types.Provider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		t.Fatalf("unmarshal providers row: %v", err)
	}
	for _, p := range providers {
		if v := p.APIKeyValue(); v != "" {
			return v
		}
	}
	return ""
}

func providerConfig(key *string) *types.Config {
	return &types.Config{Providers: []types.Provider{
		{ID: 1, Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1, APIKey: key,
			Models: []types.ModelConfig{{Name: "llama-3"}}},
	}}
}

// GetConfig is what the board, the task list and every provider lookup call, and none of them may
// receive a key.
func TestGetConfigHandsOutNoKey(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	plain := "sk-real-secret-123"
	if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
		t.Fatalf("update config: %v", err)
	}

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("want 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].APIKey != nil {
		t.Errorf("GetConfig handed out a key: %q", *cfg.Providers[0].APIKey)
	}
	if stored := firstStoredKey(t, db); !sharedSecretBox().Opens(providerKeyPurpose, "1", stored) {
		t.Errorf("stored key is not a record this box opens: %q", stored)
	}
}

func TestProviderAPIKeyOpensExactlyTheOneItIsAskedFor(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	plain := "sk-real-secret-123"
	if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != plain {
		t.Errorf("GetProviderAPIKey = %q, want the stored key", got)
	}
	// A trailing slash is the same provider; an unknown URL is no key at all.
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1/"); got != plain {
		t.Errorf("trailing slash: got %q, want the stored key", got)
	}
	if got := cs.GetProviderAPIKey("http://other:9999/v1"); got != "" {
		t.Errorf("unknown provider: got %q, want empty", got)
	}
}

// A key the process cannot open reads as no key, not as a key.
func TestUnopenableKeyReadsAsNoKey(t *testing.T) {
	otherSalt, err := crypto.New("test-passphrase", []byte("a-different-salt"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cs, db := testConfigStoreWithStore(t)
	sealed, err := otherSalt.Encrypt(providerKeyPurpose, "1", "sk-from-another-installation")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := db.SetConfigValue("providers", `[{"id":1,"name":"llm","kind":"llm","url":"http://localhost:8080/v1","concurrency":1,"api_key":"`+sealed+`"}]`); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != "" {
		t.Errorf("an unopenable key resolved to %q, want empty", got)
	}
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got == sealed {
		t.Error("the sealed text was handed out as a key")
	}
}

// A secret this build cannot open is named at startup: a plain value, one too short to be a record, or one
// sealed for another installation. What opens is not named.
func TestProvidersWithUnreadableKeys(t *testing.T) {
	elsewhere, err := crypto.New("test-passphrase", []byte("a-different-salt"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	foreign, err := elsewhere.Encrypt(providerKeyPurpose, "3", "sk-from-another-installation")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	mine, err := sharedSecretBox().Encrypt(providerKeyPurpose, "5", "sk-written-here")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	cs, db := testConfigStoreWithStore(t)
	if err := db.SetConfigValue("providers", `[
		{"id":1,"name":"plain","kind":"llm","url":"http://a/v1","models":[],"concurrency":1,"api_key":"sk-plain"},
		{"id":2,"name":"short","kind":"llm","url":"http://b/v1","models":[],"concurrency":1,"api_key":"AAA="},
		{"id":3,"name":"elsewhere","kind":"llm","url":"http://c/v1","models":[],"concurrency":1,"api_key":"`+foreign+`"},
		{"id":4,"name":"none","kind":"llm","url":"http://d/v1","models":[],"concurrency":1},
		{"id":5,"name":"ours","kind":"llm","url":"http://e/v1","models":[],"concurrency":1,"api_key":"`+mine+`"}]`); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	got := strings.Join(cs.ProvidersWithUnreadableKeys(), ",")
	if want := "plain,short,elsewhere"; got != want {
		t.Errorf("ProvidersWithUnreadableKeys = %q, want %q", got, want)
	}
}

// Omitted keeps the stored key, empty clears it, a value replaces it — confusing them is how a settings
// save eats a key.
func TestUpdateConfigKeyMeanings(t *testing.T) {
	t.Run("omitted keeps what is stored", func(t *testing.T) {
		cs, db := testConfigStoreWithStore(t)
		plain := "sk-real-secret-123"
		if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
			t.Fatalf("first save: %v", err)
		}
		before := firstStoredKey(t, db)
		if err := cs.UpdateConfig(providerConfig(nil)); err != nil {
			t.Fatalf("save without a key: %v", err)
		}
		if after := firstStoredKey(t, db); after != before {
			t.Errorf("the key changed on a save that carried none: %q -> %q", before, after)
		}
		if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != plain {
			t.Errorf("GetProviderAPIKey = %q, want the original key", got)
		}
	})

	// A client that echoes the placeholder back must not overwrite the stored key with bullets.
	t.Run("masked placeholder keeps what is stored", func(t *testing.T) {
		cs, db := testConfigStoreWithStore(t)
		plain := "sk-real-secret-123"
		if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
			t.Fatalf("first save: %v", err)
		}
		before := firstStoredKey(t, db)
		masked := maskedAPIKey
		if err := cs.UpdateConfig(providerConfig(&masked)); err != nil {
			t.Fatalf("save with the placeholder: %v", err)
		}
		if after := firstStoredKey(t, db); after != before {
			t.Error("the placeholder was stored as if it were a key")
		}
		if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != plain {
			t.Errorf("GetProviderAPIKey = %q, want the original key", got)
		}
	})

	t.Run("empty string clears it", func(t *testing.T) {
		cs, db := testConfigStoreWithStore(t)
		plain := "sk-real-secret-123"
		if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
			t.Fatalf("first save: %v", err)
		}
		clear := ""
		if err := cs.UpdateConfig(providerConfig(&clear)); err != nil {
			t.Fatalf("clearing save: %v", err)
		}
		if stored := firstStoredKey(t, db); stored != "" {
			t.Errorf("a cleared key is still stored: %q", stored)
		}
		if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != "" {
			t.Errorf("GetProviderAPIKey = %q, want empty", got)
		}
	})

	t.Run("without a key to derive there is no plaintext storage", func(t *testing.T) {
		db, err := store.NewStore(t.TempDir() + "/test.db")
		if err != nil {
			t.Fatalf("create store: %v", err)
		}
		cs := NewConfigStore(db, t.TempDir()+"/test.db", "test", nil)
		plain := "sk-must-not-be-stored-plain"
		if err := cs.UpdateConfig(providerConfig(&plain)); err == nil {
			t.Fatal("a key was accepted with no key to derive")
		}
		if raw, _ := db.GetConfigValue("providers"); strings.Contains(raw, plain) {
			t.Error("the plaintext reached the providers row")
		}
	})
}

// A read-modify-write of the providers row must go through the stored row: the config view carries no
// keys to write back.
func TestSaveModelContextWindowKeepsTheSealedKey(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	plain := "sk-real-secret-123"
	if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
		t.Fatalf("update config: %v", err)
	}
	before := firstStoredKey(t, db)

	cs.SaveModelContextWindow("llama-3", "http://localhost:8080/v1", 32768)

	if after := firstStoredKey(t, db); after != before {
		t.Errorf("the key was rewritten by a context-window save: %q -> %q", before, after)
	}
	raw, _ := db.GetConfigValue("providers")
	if strings.Contains(raw, plain) {
		t.Error("the plaintext reached the providers row")
	}
	if !strings.Contains(raw, "32768") {
		t.Error("the context window was not persisted")
	}
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != plain {
		t.Errorf("GetProviderAPIKey = %q after the save, want the original key", got)
	}
}
