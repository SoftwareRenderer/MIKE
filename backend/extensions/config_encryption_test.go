package extensions

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
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

// sealedSchema is one row holding every shape a config value arrives in: two secrets, a secret left
// blank, a plain value, and a number marked sensitive.
func sealedSchema() *ConfigSchema {
	return &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token_a", Type: "string", Title: "Token A", Sensitive: true},
			{Key: "token_b", Type: "string", Title: "Token B", Sensitive: true},
			{Key: "token_empty", Type: "string", Title: "Unused Token", Sensitive: true},
			{Key: "repo_url", Type: "string", Title: "Repo URL"},
		},
	}
}

func sealedConfig() map[string]any {
	return map[string]any{
		"token_a":     "secret-a",
		"token_b":     "secret-b",
		"token_empty": "",
		"repo_url":    "https://github.com/user/repo",
	}
}

// Every read path has to unseal what Create sealed: the board list, the card, the edit dialog and the
// LSP/agent runtime read through different queries, and a row handed back with a sealed value reads as
// a secret the person never typed. The blank secret is the case that separates the two failure
// directions — sealing "" and handing back a ciphertext, or dropping the key and losing the person's
// explicit blank.
func TestSealedConfigReadsBackOnEveryPath(t *testing.T) {
	es := testStore(t)
	ext := makeTestExtWithConfig(t, "enc-ext", "enc-ext", sealedConfig(), sealedSchema())
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := sealedConfig()
	t.Run("GetByID", func(t *testing.T) {
		got, err := es.GetByID("enc-ext")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		assertConfigReadsBack(t, got.Config, want)
	})
	t.Run("List", func(t *testing.T) {
		rows, err := es.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("List count = %d, want 1", len(rows))
		}
		assertConfigReadsBack(t, rows[0].Config, want)
	})
}

func assertConfigReadsBack(t *testing.T, got, want map[string]any) {
	t.Helper()
	for key, w := range want {
		if got[key] != w {
			t.Errorf("Config[%q] = %v (%T), want %v", key, got[key], got[key], w)
		}
	}
}

// A secret reaches the file on every write path, and the row is the only place a leak can hide: the
// Create insert and the later Update are separate statements, and forgetting the seal on either one
// stores the person's token as plaintext for as long as the row lives.
func TestNoWritePathLeavesASecretInTheClear(t *testing.T) {
	es := testStore(t)
	schema := &ConfigSchema{Properties: []SchemaProp{
		{Key: "token", Type: "string", Sensitive: true},
		{Key: "username", Type: "string"},
	}}

	ext := makeTestExtWithConfig(t, "db-ext", "db-ext", map[string]any{"token": "super-secret-key", "username": "alice"}, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertRowHoldsNoPlaintext(t, es, "db-ext", "super-secret-key")

	ext.Config = map[string]any{"token": "new-token", "username": "bob"}
	if err := es.Update("db-ext", ext); err != nil {
		t.Fatalf("Update: %v", err)
	}
	assertRowHoldsNoPlaintext(t, es, "db-ext", "new-token")

	got, err := es.GetByID("db-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Config["token"] != "new-token" || got.Config["username"] != "bob" {
		t.Errorf("read back = %v, want the updated values", got.Config)
	}
}

func assertRowHoldsNoPlaintext(t *testing.T, es *ExtensionStore, id, secret string) {
	t.Helper()
	var rawConfig string
	if err := es.db.QueryRow(`SELECT config FROM extensions WHERE id = ?`, id).Scan(&rawConfig); err != nil {
		t.Fatalf("query raw config: %v", err)
	}
	if strings.Contains(rawConfig, secret) {
		t.Fatalf("the row holds the plaintext secret after a write: %s", rawConfig)
	}
}

// With no key to seal with, the secret is dropped rather than written in the clear: a settings store
// that quietly falls back to plaintext is worse than a field that refuses to hold anything.
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

	if got.Config["api_key"] == "plain-without-key" {
		t.Error("the secret was stored in the clear")
	}
	var raw string
	if err := es.db.QueryRow(`SELECT config FROM extensions WHERE id = ?`, "nokey-ext").Scan(&raw); err != nil {
		t.Fatalf("query raw config: %v", err)
	}
	if strings.Contains(raw, "plain-without-key") {
		t.Errorf("the plaintext reached the row: %s", raw)
	}
}

// A sensitive number is sealed as its text and comes back as text, while an unsealed one keeps its
// JSON type: the edit form branches on the value's type, so silently turning 3000 into "3000" would
// be the bug — but a secret cannot keep one, because the seal is a string.
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

	if got.Config["port"] != "8080" {
		t.Errorf("port = %v (%T), want string \"8080\"", got.Config["port"], got.Config["port"])
	}
	if got.Config["visible"] != float64(3000) {
		t.Errorf("visible = %v, want 3000", got.Config["visible"])
	}
}

// What leaves the server is the same decision taken from the other side: the mask stands in for a
// stored secret, so the response carries neither the secret nor a hint of it — and only the schema
// says which field was a secret in the first place.
func TestMaskConfig(t *testing.T) {
	schema := &ConfigSchema{
		Properties: []SchemaProp{
			{Key: "token", Type: "string", Sensitive: true},
			{Key: "username", Type: "string"},
			{Key: "blank", Type: "string", Sensitive: true},
		},
	}

	// No schema: nothing declares a field secret, so nothing is masked — the alternative would hide
	// a value the person can see nowhere else.
	t.Run("a schema-less config is answered verbatim", func(t *testing.T) {
		masked := maskConfig(map[string]any{"token": "secret"}, nil)
		if masked["token"] != "secret" {
			t.Errorf("token = %v, want secret (no masking without schema)", masked["token"])
		}
	})

	t.Run("a stored secret becomes the mask and a blank stays blank", func(t *testing.T) {
		masked := maskConfig(map[string]any{
			"token": "ghp_secret123", "username": "alice", "blank": "",
		}, schema)
		if masked["token"] != types.MaskedSecret {
			t.Errorf("token = %v, want the mask", masked["token"])
		}
		if masked["username"] != "alice" {
			t.Errorf("username = %v, want alice", masked["username"])
		}
		if masked["blank"] != "" {
			t.Errorf("blank = %v, want empty: the mask would read back as a typed secret", masked["blank"])
		}
	})
}

// The form sends the mask it was handed for every field it did not touch, so the mask has to mean
// "keep what is stored" — replacing it with itself would store the mask as the person's secret.
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

	updateReq := UpdateExtensionRequest{
		Config: map[string]any{
			"token":    types.MaskedSecret, // masked placeholder sent from frontend
			"username": "bob",              // user changed this
		},
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/preserve-ext", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "preserve-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

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

// A value the manifest publishes is no secret, and a masked one leaves the pick-list unable to say
// which option was chosen. An unlisted value stays masked, and both stay sealed in the row.
func TestMaskConfig_PublishedChoiceIsNotMasked(t *testing.T) {
	es := testStore(t)
	schema := &ConfigSchema{Properties: []SchemaProp{
		{Key: "region", Type: "string", Sensitive: true, Enum: []any{"frankfurt", "ashburn"}, Default: "frankfurt"},
		{Key: "token", Type: "string", Sensitive: true},
	}}

	ext := makeTestExtWithConfig(t, "choice-ext", "choice-ext", map[string]any{
		"region": "ashburn", "token": "ghp_secret123",
	}, schema)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertRowHoldsNoPlaintext(t, es, "choice-ext", "ashburn")

	stored, err := es.GetByID("choice-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	masked := maskConfig(stored.Config, schema)
	if masked["region"] != "ashburn" {
		t.Errorf("region = %v, want the published choice the form has to show", masked["region"])
	}
	if masked["token"] != types.MaskedSecret {
		t.Errorf("token = %v, want the mask: a list decides nothing about a free-typed secret", masked["token"])
	}

	// An unlisted value predates the list, so nothing published explains it.
	if got := maskConfig(map[string]any{"region": "walldorf"}, schema)["region"]; got != types.MaskedSecret {
		t.Errorf("region = %v, want the mask for a value no list declares", got)
	}
}
