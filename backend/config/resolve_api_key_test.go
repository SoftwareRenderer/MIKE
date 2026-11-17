package config

import (
	"testing"

	"mike/backend/types"
)

// TestResolveAPIKey is the one place a request's key is decided: what the caller typed wins, and the
// placeholder or an empty field asks for the stored key instead. A provider the config does not hold, or
// one holding no key, answers empty — the placeholder must never reach a provider as a credential.
func TestResolveAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stored      *string
		serverURL   string
		providedKey string
		want        string
	}{
		{
			name:        "a key the caller typed goes out unchanged",
			stored:      ptr("sk-real-secret-123"),
			serverURL:   "http://localhost:8080/v1",
			providedKey: "sk-typed-by-user",
			want:        "sk-typed-by-user",
		},
		{
			name:        "the masked placeholder resolves the stored key",
			stored:      ptr("sk-real-secret-123"),
			serverURL:   "http://localhost:8080/v1",
			providedKey: maskedAPIKey,
			want:        "sk-real-secret-123",
		},
		{
			name:        "an empty key resolves the stored key",
			stored:      ptr("sk-real-secret-123"),
			serverURL:   "http://localhost:8080/v1",
			providedKey: "",
			want:        "sk-real-secret-123",
		},
		{
			name:        "a trailing slash is the same provider",
			stored:      ptr("sk-real-secret-123"),
			serverURL:   "http://localhost:8080/v1/",
			providedKey: maskedAPIKey,
			want:        "sk-real-secret-123",
		},
		{
			name:        "the placeholder of a provider holding no key is no key",
			serverURL:   "http://localhost:8080/v1",
			providedKey: maskedAPIKey,
			want:        "",
		},
		{
			name:        "a provider the config does not hold has no key",
			serverURL:   "http://other:9999/v1",
			providedKey: maskedAPIKey,
			want:        "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			if err := cs.UpdateConfig(&types.Config{
				Providers: []types.Provider{{
					Name: "llm-provider", Kind: types.ProviderKindLLM, URL: "http://localhost:8080/v1",
					Concurrency: 1, APIKey: tc.stored,
				}},
			}); err != nil {
				t.Fatalf("update config: %v", err)
			}

			if got := cs.ResolveAPIKey(tc.serverURL, tc.providedKey); got != tc.want {
				t.Fatalf("ResolveAPIKey(%q, %q) = %q, want %q", tc.serverURL, tc.providedKey, got, tc.want)
			}
		})
	}
}
