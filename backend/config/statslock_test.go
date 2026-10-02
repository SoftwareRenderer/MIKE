package config

import (
	"encoding/json"
	"testing"
)

// statResetLockedStore is a store built the way a process with the switch on builds one.
func statResetLockedStore(t *testing.T) *ConfigStore {
	t.Helper()
	_, db := testConfigStoreWithStore(t)
	return NewConfigStore(db, "", "test", sharedSecretBox(), false, true)
}

// The flag rides the config view, because that is where the UI learns to hide the reset.
func TestConfigViewReportsTheStatResetLock(t *testing.T) {
	marshal := func(t *testing.T, cs *ConfigStore) any {
		t.Helper()
		cfg, err := cs.GetConfig()
		if err != nil {
			t.Fatalf("get config: %v", err)
		}
		raw, err := json.Marshal(cs.buildConfigResponse(cfg))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out["stats_reset_restricted"]
	}

	if got := marshal(t, testConfigStore(t)); got != false {
		t.Errorf("stats_reset_restricted on a store booted without the switch = %v, want false", got)
	}
	if got := marshal(t, statResetLockedStore(t)); got != true {
		t.Errorf("stats_reset_restricted under %s = %v, want true", EnvRestrictStatReset, got)
	}
}
