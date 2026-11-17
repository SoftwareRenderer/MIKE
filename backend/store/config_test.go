package store

import (
	"testing"
)

// The config table holds one value per key: a write replaces what was there rather than adding a second
// row, an absent key answers an error rather than the empty string a caller could mistake for a stored
// blank, and a stored blank reads back blank.
func TestConfigValuesAreOnePerKey(t *testing.T) {
	s := testStore(t)

	if err := s.SetConfigValue("only_key", "first"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if err := s.SetConfigValue("only_key", "second"); err != nil {
		t.Fatalf("re-set config: %v", err)
	}
	if err := s.SetConfigValue("blank_key", ""); err != nil {
		t.Fatalf("set the blank: %v", err)
	}

	for _, tc := range []struct {
		name string
		key  string
		want string
	}{
		{"a key written twice keeps the last value", "only_key", "second"},
		{"a value stored empty reads back empty, not absent", "blank_key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.GetConfigValue(tc.key)
			if err != nil {
				t.Fatalf("get %s: %v", tc.key, err)
			}
			if got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}

	t.Run("a key with no row answers an error", func(t *testing.T) {
		if _, err := s.GetConfigValue("no_such_key"); err == nil {
			t.Fatal("an absent key read back as a value, which a caller cannot tell from a stored blank")
		}
	})
}
