package extensions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeTestExtWithConfig(t *testing.T, id, name string, config map[string]any, schema *ConfigSchema) *Extension {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	return &Extension{
		ID:            id,
		Name:          name,
		Version:       "1.0.0",
		Type:          ExtensionTypeUI,
		Manifest:      ExtensionManifest{DisplayName: name, ConfigSchema: schema},
		Config:        config,
		InstallSource: "manual",
		Enabled:       true,
		InstalledAt:   now,
		BuiltAt:       now,
	}
}

func TestConfigEncryption_RoundTrip(t *testing.T) {
	es := testStore(t)

	// Schema marks github_token as sensitive
	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "github_token",
				Type:      "string",
				Title:     "GitHub Token",
				Sensitive: true,
			},
			{Key: "repo_url",
				Type:  "string",
				Title: "Repo URL",
			},
		},
	}

	config := map[string]any{
		"github_token": "ghp_secret123",
		"repo_url":     "https://github.com/user/repo",
	}

	ext := makeTestExtWithConfig(t, "enc-ext", "enc-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("enc-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Sensitive value should be decrypted
	if got.Config["github_token"] != "ghp_secret123" {
		t.Errorf("github_token = %v, want ghp_secret123", got.Config["github_token"])
	}

	// Non-sensitive value should be unchanged
	if got.Config["repo_url"] != "https://github.com/user/repo" {
		t.Errorf("repo_url = %v, want https://github.com/user/repo", got.Config["repo_url"])
	}
}

func TestConfigEncryption_EncryptedInDB(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "api_key",
				Type:      "string",
				Title:     "API Key",
				Sensitive: true,
			},
		},
	}

	config := map[string]any{
		"api_key": "super-secret-key",
	}

	ext := makeTestExtWithConfig(t, "db-ext", "db-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Read raw JSON from DB to verify encryption
	var rawConfig string
	err := es.db.QueryRow(`SELECT config FROM extensions WHERE id = ?`, "db-ext").Scan(&rawConfig)
	if err != nil {
		t.Fatalf("query raw config: %v", err)
	}

	// The raw config should NOT contain the plaintext secret
	if configContains(rawConfig, "super-secret-key") {
		t.Fatal("raw DB config contains plaintext secret — encryption did not work")
	}
}

func TestConfigEncryption_NoSchema(t *testing.T) {
	es := testStore(t)

	// No config_schema — values stored as-is
	config := map[string]any{
		"api_key": "plain-value",
	}

	ext := makeTestExtWithConfig(t, "noschema-ext", "noschema-ext", config, nil)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("noschema-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Without schema, value is stored as plain text
	if got.Config["api_key"] != "plain-value" {
		t.Errorf("api_key = %v, want plain-value", got.Config["api_key"])
	}
}

func TestConfigEncryption_NoKey(t *testing.T) {
	es := testStoreNoKey(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "api_key",
				Type:      "string",
				Title:     "API Key",
				Sensitive: true,
			},
		},
	}

	config := map[string]any{
		"api_key": "plain-without-key",
	}

	ext := makeTestExtWithConfig(t, "nokey-ext", "nokey-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("nokey-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Nothing to seal with, so the secret is dropped rather than stored in the clear.
	if got.Config["api_key"] == "plain-without-key" {
		t.Error("the secret was stored in the clear")
	}
	var raw string
	if err := es.db.QueryRow(`SELECT config FROM extensions WHERE id = ?`, "nokey-ext").Scan(&raw); err != nil {
		t.Fatalf("query raw config: %v", err)
	}
	if configContains(raw, "plain-without-key") {
		t.Errorf("the plaintext reached the row: %s", raw)
	}
}

func TestConfigEncryption_UpdatePreservesEncryption(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
			{Key: "username", Type: "string"},
		},
	}

	config := map[string]any{
		"token":    "old-token",
		"username": "alice",
	}

	ext := makeTestExtWithConfig(t, "upd-ext", "upd-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with new values
	ext.Config = map[string]any{
		"token":    "new-token",
		"username": "bob",
	}
	if err := es.Update("upd-ext", ext); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := es.GetByID("upd-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.Config["token"] != "new-token" {
		t.Errorf("token = %v, want new-token", got.Config["token"])
	}
	if got.Config["username"] != "bob" {
		t.Errorf("username = %v, want bob", got.Config["username"])
	}
}

func TestConfigEncryption_ListAlsoDecrypts(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "secret", Type: "string", Sensitive: true},
		},
	}

	config := map[string]any{
		"secret": "list-secret",
	}

	ext := makeTestExtWithConfig(t, "list-ext", "list-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Use List instead of GetByID
	extensions, err := es.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(extensions) != 1 {
		t.Fatalf("List count = %v, want 1", len(extensions))
	}

	if extensions[0].Config["secret"] != "list-secret" {
		t.Errorf("secret = %v, want list-secret", extensions[0].Config["secret"])
	}
}

func TestConfigEncryption_MultipleSensitiveFields(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token_a", Type: "string", Sensitive: true},
			{Key: "token_b", Type: "string", Sensitive: true},
			{Key: "publicVal", Type: "string"},
		},
	}

	config := map[string]any{
		"token_a":   "secret-a",
		"token_b":   "secret-b",
		"publicVal": "visible",
	}

	ext := makeTestExtWithConfig(t, "multi-ext", "multi-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("multi-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.Config["token_a"] != "secret-a" {
		t.Errorf("token_a = %v, want secret-a", got.Config["token_a"])
	}
	if got.Config["token_b"] != "secret-b" {
		t.Errorf("token_b = %v, want secret-b", got.Config["token_b"])
	}
	if got.Config["publicVal"] != "visible" {
		t.Errorf("publicVal = %v, want visible", got.Config["publicVal"])
	}
}

func TestConfigEncryption_EmptySensitiveValue(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
		},
	}

	config := map[string]any{
		"token": "",
	}

	ext := makeTestExtWithConfig(t, "empty-ext", "empty-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("empty-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.Config["token"] != "" {
		t.Errorf("token = %v, want empty string", got.Config["token"])
	}
}

func TestConfigEncryption_NonStringSensitiveValue(t *testing.T) {
	es := testStore(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "port", Type: "number", Sensitive: true},
			{Key: "visible", Type: "number"},
		},
	}

	config := map[string]any{
		"port":    float64(8080),
		"visible": float64(3000),
	}

	ext := makeTestExtWithConfig(t, "num-ext", "num-ext", config, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("num-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Sensitive numeric values are encrypted as strings and returned as strings
	if got.Config["port"] != "8080" {
		t.Errorf("port = %v (%T), want string \"8080\"", got.Config["port"], got.Config["port"])
	}

	// Non-sensitive values keep their original type
	if got.Config["visible"] != float64(3000) {
		t.Errorf("visible = %v, want 3000", got.Config["visible"])
	}
}

func configContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMaskConfig(t *testing.T) {
	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
			{Key: "username", Type: "string"},
		},
	}

	config := map[string]any{
		"token":    "ghp_secret123",
		"username": "alice",
	}

	masked := maskConfig(config, schema)

	// Sensitive value should be masked
	if masked["token"] != maskedValue {
		t.Errorf("token = %v, want masked value", masked["token"])
	}

	// Non-sensitive value should be unchanged
	if masked["username"] != "alice" {
		t.Errorf("username = %v, want alice", masked["username"])
	}
}

func TestMaskConfig_NoSchema(t *testing.T) {
	config := map[string]any{"token": "secret"}
	masked := maskConfig(config, nil)

	if masked["token"] != "secret" {
		t.Errorf("token = %v, want secret (no masking without schema)", masked["token"])
	}
}

func TestMaskConfig_EmptySensitiveValue(t *testing.T) {
	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
		},
	}

	config := map[string]any{"token": ""}
	masked := maskConfig(config, schema)

	// Empty sensitive value should remain empty (not masked)
	if masked["token"] != "" {
		t.Errorf("token = %v, want empty string", masked["token"])
	}
}

func TestMaskConfig_PreservedOnUpdate(t *testing.T) {
	h := testHandler(t)

	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
			{Key: "username", Type: "string"},
		},
	}

	config := map[string]any{
		"token":    "original-secret",
		"username": "alice",
	}

	ext := makeTestExtWithConfig(t, "preserve-ext", "preserve-ext", config, schema)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate frontend update: masked token + changed username
	// This is what happens when user edits only the username field
	updateReq := UpdateExtensionRequest{
		Config: map[string]any{
			"token":    maskedValue, // masked placeholder sent from frontend
			"username": "bob",       // user changed this
		},
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/preserve-ext", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "preserve-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Read back — token should still be the original secret
	got, err := h.store.GetByID("preserve-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.Config["token"] != "original-secret" {
		t.Errorf("token = %v, want original-secret (masked value should be preserved)", got.Config["token"])
	}
	if got.Config["username"] != "bob" {
		t.Errorf("username = %v, want bob", got.Config["username"])
	}
}
