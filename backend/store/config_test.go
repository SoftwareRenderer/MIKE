package store

import (
	"testing"
)

func TestGetSetConfigValue(t *testing.T) {
	s := testStore(t)

	if err := s.SetConfigValue("test_key", "test_value"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	// Get it back
	val, err := s.GetConfigValue("test_key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if val != "test_value" {
		t.Fatalf("unexpected value: %s", val)
	}
}

func TestGetConfigValueNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetConfigValue("nonexistent_key")
	if err == nil {
		t.Fatal("expected error for missing config key")
	}
}

func TestConfigOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SetConfigValue("key", "value1"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if err := s.SetConfigValue("key", "value2"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	val, err := s.GetConfigValue("key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if val != "value2" {
		t.Fatalf("expected value2, got %s", val)
	}
}
