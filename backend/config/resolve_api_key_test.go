package config

import (
	"testing"

	"mike/backend/types"
)

func TestResolveAPIKey(t *testing.T) {
	cs := testConfigStore(t)

	storedKey := "sk-real-secret-123"
	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm-provider", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	cfg.Providers[0].APIKey = &storedKey
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	tests := []struct {
		name        string
		serverURL   string
		providedKey string
		want        string
	}{
		{
			name:        "real key passed through unchanged",
			serverURL:   "http://localhost:8080/v1",
			providedKey: "sk-typed-by-user",
			want:        "sk-typed-by-user",
		},
		{
			name:        "masked placeholder resolves stored key",
			serverURL:   "http://localhost:8080/v1",
			providedKey: maskedAPIKey,
			want:        storedKey,
		},
		{
			name:        "empty key resolves stored key",
			serverURL:   "http://localhost:8080/v1",
			providedKey: "",
			want:        storedKey,
		},
		{
			name:        "trailing slash normalized when resolving",
			serverURL:   "http://localhost:8080/v1/",
			providedKey: maskedAPIKey,
			want:        storedKey,
		},
		{
			name:        "no matching provider returns empty",
			serverURL:   "http://other:9999/v1",
			providedKey: maskedAPIKey,
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cs.ResolveAPIKey(tt.serverURL, tt.providedKey)
			if got != tt.want {
				t.Fatalf("ResolveAPIKey(%q, %q) = %q, want %q", tt.serverURL, tt.providedKey, got, tt.want)
			}
		})
	}
}

func TestResolveAPIKeyNoStoredKey(t *testing.T) {
	cs := testConfigStore(t)

	// Provider exists but has no stored key.
	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm-provider", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	got := cs.ResolveAPIKey("http://localhost:8080/v1", maskedAPIKey)
	if got != "" {
		t.Fatalf("ResolveAPIKey with no stored key = %q, want empty", got)
	}
}
