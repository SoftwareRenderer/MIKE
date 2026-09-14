package exec

// Every TaskExecutor in this package's tests is assembled by NewTaskExecutor. The wiring the server
// depends on — the configured worktree lock, the merge and summarize orchestrators, the lazy-start
// handler — is therefore the wiring under test, and a field can move without rewriting literals.

import (
	"fmt"
	"path/filepath"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/stream"
)

// newTestExecutorWith builds an executor through the production constructor. A dependency a test has
// no use for stays nil: the nil is what the code path under test expects, so a test of a host-side
// tool gets no store and no sandbox manager.
//
// A collaborator the constructor takes concretely (the LSP manager, a sandbox manager's binary probe)
// is replaced by assigning the field afterwards, which is also how a test states which single
// collaborator it is faking.
func newTestExecutorWith(t testing.TB, deps Deps) *TaskExecutor {
	t.Helper()
	if deps.AppStreamMgr == nil {
		deps.AppStreamMgr = newTestStreamMgr(t, deps.Store, deps.WorktreeMgr)
	}
	return NewTaskExecutor(deps)
}

// newBareTestExecutor is an executor with no dependency beyond the stream manager the constructor
// requires, for a tool or helper that receives everything it acts on as an argument.
func newBareTestExecutor(t testing.TB) *TaskExecutor {
	t.Helper()
	return newTestExecutorWith(t, Deps{})
}

// newTestStreamMgr builds a manager over s, released when the test ends: each one holds filesystem
// watches. worktreeMgr is what a fixture that wants its worktrees actually scanned passes.
func newTestStreamMgr(t testing.TB, s *store.Store, worktreeMgr *git.WorktreeManager) *stream.AppStreamManager {
	t.Helper()
	asm := stream.NewAppStreamManager(s, worktreeMgr)
	t.Cleanup(asm.Close)
	return asm
}

// newTestConfigStore is a config store over s with a workspace of its own and no providers, so a
// provider lookup resolves nowhere unless the test seeds one.
func newTestConfigStore(t testing.TB, s *store.Store) *config.ConfigStore {
	t.Helper()
	cs := config.NewConfigStore(s, "", "test")
	cs.SetWorkspaceDirOverride(t.TempDir())
	if err := s.SetConfigValue("providers", "[]"); err != nil {
		t.Fatalf("set config providers: %v", err)
	}
	for _, k := range []string{"worker_provider", "worker_model", "utility_provider", "utility_model"} {
		if err := s.SetConfigValue(k, ""); err != nil {
			t.Fatalf("set config %s: %v", k, err)
		}
	}
	return cs
}

// testStore creates a new Store with a temporary database for testing.
func testStore(t testing.TB) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTestExecutor is the common fixture: a real store, a config store with no providers, and a
// workspace directory of its own.
func newTestExecutor(t testing.TB) *TaskExecutor {
	t.Helper()
	te, _ := newTestExecutorWithStore(t)
	return te
}

// newTestExecutorWithStore also returns the store, for tests that seed rows themselves.
func newTestExecutorWithStore(t testing.TB) (*TaskExecutor, *store.Store) {
	t.Helper()
	s := testStore(t)
	return newTestExecutorWith(t, Deps{Store: s, ConfigStore: newTestConfigStore(t, s)}), s
}

// newTestExecutorWithConfig creates a TaskExecutor with config defaults set.
func newTestExecutorWithConfig(t testing.TB, autoMerge, subtasksAutoMerge bool) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	cs := newTestConfigStore(t, s)
	for k, v := range map[string]string{
		"default_auto_merge":          fmt.Sprintf("%t", autoMerge),
		"default_subtasks_auto_merge": fmt.Sprintf("%t", subtasksAutoMerge),
	} {
		if err := s.SetConfigValue(k, v); err != nil {
			t.Fatalf("set config %s: %v", k, err)
		}
	}
	return newTestExecutorWith(t, Deps{Store: s, ConfigStore: cs})
}
