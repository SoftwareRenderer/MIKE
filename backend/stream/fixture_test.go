package stream

// The fixture every watcher test starts from. Each one used to carry its own copy of "init a repo,
// commit one file, open a database, point the config store at an empty worktrees directory, register
// the project", and the copies drifted — one wrote the config row, another did not, so two tests
// believed they ran on the same fixture when only one of them had a stored config at all.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
	"mike/backend/types"
)

// watcherFixture is one project on one committed repository, with nowhere to put worktrees until the
// test asks for one.
type watcherFixture struct {
	RepoDir      string
	WorktreesDir string
	Store        *store.Store
	Config       *config.ConfigStore
	// MainBranch is the branch the root commit landed on, which the machine's init.defaultBranch
	// decides; a task that names a base ref needs it rather than a guessed "main".
	MainBranch string
}

// newWatcherFixture builds that project under a temporary directory of its own. The id and name are
// the caller's because several tests read the project back by id and one asserts on the name.
func newWatcherFixture(t *testing.T, projectID, projectName string) *watcherFixture {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	for _, dir := range []string{repoDir, worktreesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testgit.RepoAtMessage(t, repoDir, "Initial")
	main := strings.TrimSpace(testgit.GitOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))

	dbPath := filepath.Join(tmpDir, "test.db")
	s := teststore.StoreIn(t, dbPath)
	cfgStore := teststore.ConfigStore(t, s, dbPath)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	// Written rather than left absent: the worktree manager resolves a project's worktree path
	// through the stored config, and a fixture that never stored one was relying on the same
	// fallback a misconfigured server would take.
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	if err := s.CreateProject(&types.Project{ID: projectID, Name: projectName, Path: repoDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	return &watcherFixture{
		RepoDir:      repoDir,
		WorktreesDir: worktreesDir,
		Store:        s,
		Config:       cfgStore,
		MainBranch:   main,
	}
}
