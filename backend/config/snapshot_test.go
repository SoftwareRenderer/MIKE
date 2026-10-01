package config

import (
	"testing"

	"mike/backend/store"
	"mike/backend/types"
)

// countingStore counts the reads a config read performs, which is the claim under test.
type countingStore struct {
	*store.Store
	batchReads  int
	singleReads int
}

func (c *countingStore) GetConfigValues(keys ...string) (map[string]string, error) {
	c.batchReads++
	return c.Store.GetConfigValues(keys...)
}

func (c *countingStore) GetConfigValue(key string) (string, error) {
	c.singleReads++
	return c.Store.GetConfigValue(key)
}

func newCountingStore(t *testing.T) (*ConfigStore, *countingStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	counter := &countingStore{Store: db}
	return NewConfigStore(counter, dir+"/test.db", "test", sharedSecretBox(), false), counter
}

// One query for the rows a config read wants, and none once the snapshot is warm.
func TestConfigReadIsOneQueryAndCached(t *testing.T) {
	cs, counter := newCountingStore(t)

	if _, err := cs.GetConfig(); err != nil {
		t.Fatalf("get config: %v", err)
	}
	if counter.batchReads != 1 || counter.singleReads != 0 {
		t.Fatalf("first read: %d batch and %d single-row queries, want 1 and 0", counter.batchReads, counter.singleReads)
	}

	for i := 0; i < 5; i++ {
		if _, err := cs.GetConfig(); err != nil {
			t.Fatalf("get config: %v", err)
		}
	}
	if counter.batchReads != 1 {
		t.Errorf("6 reads performed %d queries, want the snapshot to answer 5 of them", counter.batchReads)
	}
}

// The cache is dropped by the write counter, not a clock: a save is visible to the next read.
func TestSnapshotSeesAWriteImmediately(t *testing.T) {
	cs, counter := newCountingStore(t)
	if _, err := cs.GetConfig(); err != nil {
		t.Fatalf("get config: %v", err)
	}

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkerModel = "worker-two"
	cfg.Providers = []types.Provider{{ID: 1, Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1}}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config after the save: %v", err)
	}
	if read.WorkerModel != "worker-two" {
		t.Errorf("WorkerModel = %q after the save, want the saved value", read.WorkerModel)
	}
	if counter.batchReads < 2 {
		t.Errorf("%d queries for a read-save-read, want the save to invalidate the snapshot", counter.batchReads)
	}
}

// A row written by anything outside this package still invalidates, because the counter lives with the
// write.
func TestSnapshotSeesAWriteFromElsewhere(t *testing.T) {
	cs, counter := newCountingStore(t)
	if _, err := cs.GetConfig(); err != nil {
		t.Fatalf("get config: %v", err)
	}
	before := counter.batchReads

	if err := counter.SetConfigValue("worker_model", "written-by-another-package"); err != nil {
		t.Fatalf("set config value: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.WorkerModel != "written-by-another-package" {
		t.Errorf("WorkerModel = %q, want the value written outside this package", read.WorkerModel)
	}
	if counter.batchReads == before {
		t.Error("the snapshot survived a write it never saw")
	}
}

// A cached provider list is not writable through: a caller changes its own copy only.
func TestCachedProvidersAreNotSharedMutable(t *testing.T) {
	cs, db := newCountingStore(t)
	plain := "sk-real-secret-123"
	if err := cs.UpdateConfig(providerConfig(&plain)); err != nil {
		t.Fatalf("update config: %v", err)
	}

	first, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	first.Providers[0].URL = "http://mutated/v1"

	second, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if second.Providers[0].URL != "http://localhost:8080/v1" {
		t.Errorf("a caller's mutation reached the next read: URL = %q", second.Providers[0].URL)
	}
	if got := firstStoredKey(t, db.Store); got == "" {
		t.Error("the stored key disappeared")
	}
}
