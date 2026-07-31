package crypto

import (
	"os"
	"testing"
)

func TestWarnWeakSecrets(t *testing.T) {
	tests := []struct {
		name           string
		encKey         string
		loginPassword  string
		wantWeak       []string
	}{
		{
			name:          "both strong",
			encKey:        "strong-enough-key",
			loginPassword: "strong-password",
			wantWeak:      nil,
		},
		{
			name:          "both weak",
			encKey:        "short",
			loginPassword: "weak",
			wantWeak:      []string{"MIKE_ENCRYPTION_KEY", "MIKE_LOGIN_PASSWORD"},
		},
		{
			name:          "encryption key weak",
			encKey:        "1234567",
			loginPassword: "strong-enough-password",
			wantWeak:      []string{"MIKE_ENCRYPTION_KEY"},
		},
		{
			name:          "login password weak",
			encKey:        "strong-enough-key-value",
			loginPassword: "abc",
			wantWeak:      []string{"MIKE_LOGIN_PASSWORD"},
		},
		{
			name:          "exactly 8 chars is ok",
			encKey:        "12345678",
			loginPassword: "password",
			wantWeak:      nil,
		},
		{
			name:          "exactly 7 chars is weak",
			encKey:        "1234567",
			loginPassword: "passwor",
			wantWeak:      []string{"MIKE_ENCRYPTION_KEY", "MIKE_LOGIN_PASSWORD"},
		},
		{
			name:          "empty env vars are ignored",
			encKey:        "",
			loginPassword: "",
			wantWeak:      nil,
		},
		{
			name:          "one set one empty",
			encKey:        "weak",
			loginPassword: "",
			wantWeak:      []string{"MIKE_ENCRYPTION_KEY"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Save and restore env vars
			oldEnc := os.Getenv("MIKE_ENCRYPTION_KEY")
			oldLogin := os.Getenv("MIKE_LOGIN_PASSWORD")
			defer func() {
				os.Setenv("MIKE_ENCRYPTION_KEY", oldEnc)
				os.Setenv("MIKE_LOGIN_PASSWORD", oldLogin)
			}()

			os.Setenv("MIKE_ENCRYPTION_KEY", tc.encKey)
			os.Setenv("MIKE_LOGIN_PASSWORD", tc.loginPassword)

			got := WarnWeakSecrets()

			if len(got) != len(tc.wantWeak) {
				t.Fatalf("got %d weak secrets, want %d: got=%v, want=%v", len(got), len(tc.wantWeak), got, tc.wantWeak)
			}
			for i, name := range got {
				if name != tc.wantWeak[i] {
					t.Errorf("got[%d] = %q, want %q", i, name, tc.wantWeak[i])
				}
			}
		})
	}
}
