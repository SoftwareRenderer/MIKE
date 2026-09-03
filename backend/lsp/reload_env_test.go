package lsp

// A language server over a bind mount gets no file-watch events, so the only thing that tells it
// the project changed is the turn's own changed-file list. These pin the two halves of that:
// which file means "re-read the project", and ending *that* workspace's connection — no other's.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/store"
	"kanban/backend/types"
)

func TestAdaptProjectMountsDropsACoreContainerPath(t *testing.T) {
	// Two -v specs on one container path make podman refuse the create, so a borrowed mount that wants the
	// granted dependency directory has to give way.
	root := t.TempDir()
	projPath := filepath.Join(root, "MIKE")
	wsPath := filepath.Join(projPath, "worktrees", "MIKE", "flint")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	projStore, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projStore.Close() })
	if err := projStore.CreateProject(&types.Project{
		ID:   "MIKE",
		Name: "MIKE",
		Path: projPath,
		SandboxMounts: []types.SandboxMount{
			{HostPath: "/tmp/kanban-mount/mod", ContainerPath: "/go/pkg/mod", Mode: "rw"},
			{HostPath: "/tmp/kanban-mount/go", ContainerPath: "/go", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewExtensionLSPManager(nil, nil, projStore)
	reserved := []types.SandboxMount{{HostPath: "/host/gomodcache", ContainerPath: "/go/pkg/mod", Mode: "ro"}}

	got := mgr.adaptProjectMounts(wsPath, "/mnt/flint", reserved)
	var paths []string
	for _, mo := range got {
		if mo.ContainerPath == "/go/pkg/mod" {
			t.Errorf("the granted dependency directory was also borrowed from the project: %+v", mo)
		}
		paths = append(paths, mo.ContainerPath)
	}
	// A container path that merely *contains* core's is a different mount point, and nested binds
	// are legal — so this must not widen into "drop anything under a core path".
	found := false
	for _, p := range paths {
		if p == "/go" {
			found = true
		}
	}
	if !found {
		t.Errorf("a mount that only contains core's container path was dropped: %v", paths)
	}

	// Nothing reserved, nothing dropped: the same call without a grant keeps every mount it had.
	if all := mgr.adaptProjectMounts(wsPath, "/mnt/flint", nil); len(all) != 2 {
		t.Errorf("without a reserved path adaptProjectMounts returned %v, want both mounts", all)
	}
	if !isReservedContainerPath("/go/pkg/mod", reserved) || isReservedContainerPath("/go", reserved) {
		t.Error("isReservedContainerPath must be exact equality, not a prefix test")
	}
}

func TestReloadNeeded(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		changed  string
		want     bool
	}{
		{"declared nothing", nil, "go.mod", false},
		{"exact match", []string{"go.mod"}, "go.mod", true},
		{"nested module of the same name", []string{"go.mod"}, "internal/foo/go.mod", true},
		{"cleaned path", []string{"go.mod"}, "./go.mod", true},
		{"lockfile declared by path", []string{"pnpm-lock.yaml"}, "pnpm-lock.yaml", true},
		{"unrelated file", []string{"go.mod"}, "main.go", false},
		{"directory itself", []string{"go.mod"}, "go", false},
		{"empty or dot", []string{"go.mod"}, "", false},
		{"blank declaration is skipped", []string{"  "}, "go.mod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reloadNeeded(tc.declared, tc.changed); got != tc.want {
				t.Errorf("reloadNeeded(%v, %q) = %v, want %v", tc.declared, tc.changed, got, tc.want)
			}
		})
	}
}

func TestFilesChangedEndsOnlyTheWorkspaceWhoseProjectChanged(t *testing.T) {
	m := NewExtensionLSPManager(nil, nil, nil)
	ext := &extensions.Extension{ID: "go-team-gopls"}
	ext.SandboxConfig.ReloadFiles = []string{"go.mod"}

	changed := newSandboxClient(nil, "/ws/a", "go", ext.ID, map[string]string{})
	changed.extension = ext
	untouched := newSandboxClient(nil, "/ws/b", "go", ext.ID, map[string]string{})
	untouched.extension = ext
	// A verdict exists for the workspace being recycled; it describes a server that is about
	// to be gone, so it must not outlive the connection.
	m.environments[processKey("/ws/a", "go")] = envStatus{healthy: false, checked: true}

	m.clients[processKey("/ws/a", "go")] = changed
	m.clients[processKey("/ws/b", "go")] = untouched

	if !m.FilesChanged("/ws/a", []string{"go.mod"}) {
		t.Fatal("FilesChanged reported nothing was recycled")
	}
	if !changed.dead.Load() {
		t.Error("the changed workspace's connection must be marked dead so its holders re-attach")
	}
	if _, ok := m.clients[processKey("/ws/a", "go")]; ok {
		t.Error("the recycled connection is still installed — its holders would reuse a server with the old graph")
	}
	if _, ok := m.environments[processKey("/ws/a", "go")]; ok {
		t.Error("the stale environment verdict outlived its connection")
	}
	if untouched.dead.Load() {
		t.Error("another workspace's connection was ended — it did not change its project")
	}
	if _, ok := m.clients[processKey("/ws/b", "go")]; !ok {
		t.Error("the untouched workspace lost its connection")
	}
}

func TestFilesChangedIgnoresAFileTheExtensionDidNotDeclare(t *testing.T) {
	m := NewExtensionLSPManager(nil, nil, nil)
	ext := &extensions.Extension{ID: "go-team-gopls"}
	ext.SandboxConfig.ReloadFiles = []string{"go.mod"}
	sc := newSandboxClient(nil, "/ws/a", "go", ext.ID, map[string]string{})
	sc.extension = ext
	m.clients[processKey("/ws/a", "go")] = sc

	if m.FilesChanged("/ws/a", []string{"main.go"}) {
		t.Error("an ordinary source file must not recycle the server — that is a cold start per turn")
	}
	if sc.dead.Load() {
		t.Error("the connection was ended for a file that does not change the project")
	}
	if m.FilesChanged("/ws/a", nil) {
		t.Error("an empty changed list cannot mean anything")
	}
}

func TestConnectionCarriesItsExtensionsGate(t *testing.T) {
	// The gate travels with the connection because it is the *server* that may be unable to
	// load the workspace; EnvironmentSummary reads it back off the live connections.
	m := NewExtensionLSPManager(nil, nil, nil)
	ext := &extensions.Extension{ID: "go-team-gopls"}
	ext.SandboxConfig.Environment = &extensions.EnvironmentGate{
		Note:          "gopls cannot see this project's packages",
		SuppressCodes: []string{"BrokenImport"},
	}
	sc := newSandboxClient(nil, "/ws/a", "go", ext.ID, map[string]string{})
	sc.extension = ext
	sc.gate = ext.SandboxConfig.Environment
	m.clients[processKey("/ws/a", "go")] = sc
	// The check has actually run: "no verdict yet" and "verdict: cannot resolve" are different
	// states, and only the second is worth telling the user about.
	m.environments[processKey("/ws/a", "go")] = envStatus{healthy: false, checked: true, checkedAt: time.Now()}

	healthy, note := m.EnvironmentSummary(ext.ID)
	if healthy || note != ext.SandboxConfig.Environment.Note {
		t.Errorf("EnvironmentSummary = (%v, %q), want unhealthy with the manifest's note", healthy, note)
	}

	// An extension that never ran its check reports nothing — the absence of a verdict is not
	// evidence that the project is broken.
	if h, n := m.EnvironmentSummary("other-ext"); !h || n != "" {
		t.Errorf("unknown extension reported (%v, %q), want (true, \"\")", h, n)
	}
}
