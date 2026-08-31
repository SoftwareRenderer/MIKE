package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/store"
	"kanban/backend/types"
)

// --- resolveDetectedPath: the whole safety story in one pure function --------

func TestResolveDetectedPathAcceptsOnlyAConfiguredMount(t *testing.T) {
	mounts := []types.SandboxMount{
		{HostPath: "/host/go", ContainerPath: "/go", Mode: "ro"},
		{HostPath: "/host/alpha/node", ContainerPath: "/home/sandbox/alpha/frontend/node_modules", Mode: "ro"},
	}

	ok := []struct {
		name          string
		raw           string
		wantContainer string
		wantHost      string
	}{
		{"inside a mount", "/go/pkg/mod", "/go/pkg/mod", "/host/go/pkg/mod"},
		{"trailing newline from the probe", "/go/pkg/mod\n", "/go/pkg/mod", "/host/go/pkg/mod"},
		{"the mount itself", "/go", "/go", "/host/go"},
		{"trailing slash", "/go/pkg/mod/", "/go/pkg/mod", "/host/go/pkg/mod"},
		{"deeply nested", "/home/sandbox/alpha/frontend/node_modules/@scope/pkg",
			"/home/sandbox/alpha/frontend/node_modules/@scope/pkg", "/host/alpha/node/@scope/pkg"},
		{"redundant segments cleaned", "/go/pkg/./mod", "/go/pkg/mod", "/host/go/pkg/mod"},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			container, host, err := resolveDetectedPath(tc.raw, mounts)
			if err != nil {
				t.Fatalf("rejected a usable answer: %v", err)
			}
			if container != tc.wantContainer || host != tc.wantHost {
				t.Errorf("got (%q, %q), want (%q, %q)", container, host, tc.wantContainer, tc.wantHost)
			}
		})
	}
}

func TestResolveDetectedPathRejectsEverythingElse(t *testing.T) {
	mounts := []types.SandboxMount{{HostPath: "/host/go", ContainerPath: "/go", Mode: "ro"}}

	bad := []struct{ name, raw string }{
		{"empty", ""},
		{"whitespace only", "   \n"},
		{"relative", "pkg/mod"},
		{"relative with dotdot", "../pkg/mod"},
		{"multi-line answer", "/go/pkg/mod\n/go/pkg/sumdb"},
		{"colon would rewrite a mount spec", "/go/pkg:mod"},
		{"control character", "/go/pkg\x07mod"},
		{"filesystem root", "/"},
		{"path outside every mount", "/etc/shadow"},
		{"escapes its mount", "/go/../../etc"},
		{"lookalike sibling mount", "/gomod/pkg"},
		{"oversized", "/" + strings.Repeat("a", detectMaxValueLen)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if c, h, err := resolveDetectedPath(tc.raw, mounts); err == nil {
				t.Errorf("accepted %q as (%q, %q)", tc.raw, c, h)
			} else if !errors.Is(err, errNoDetection) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestResolveDetectedPathLongestMountWins(t *testing.T) {
	// /go/pkg/mod is mounted from a different host directory than /go, so the
	// narrower entry has to answer — otherwise the host-side existence check looks
	// in the wrong place and a real cache is reported missing.
	mounts := []types.SandboxMount{
		{HostPath: "/host/go", ContainerPath: "/go"},
		{HostPath: "/host/other/mod", ContainerPath: "/go/pkg/mod"},
	}
	container, host, err := resolveDetectedPath("/go/pkg/mod/cache/download", mounts)
	if err != nil {
		t.Fatal(err)
	}
	if container != "/go/pkg/mod/cache/download" {
		t.Errorf("container path rewritten: %q", container)
	}
	if host != "/host/other/mod/cache/download" {
		t.Errorf("host path reverse-mapped through the wrong mount: %q", host)
	}
}

// --- applyDetectedValue ------------------------------------------------------

func TestApplyDetectedValueSubstitutesAndRequiresHostDir(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "pkg", "mod")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	mounts := []types.SandboxMount{{HostPath: root, ContainerPath: "/go"}}
	probe := extensions.DetectProbe{
		CommandRef: extensions.CommandRef{Command: "go", Args: []string{"env", "GOMODCACHE"}},
		Envs:       []types.SandboxEnv{{Key: "GOMODCACHE", Value: "{value}"}},
	}

	envs, err := applyDetectedValue(probe, "/go/pkg/mod", mounts)
	if err != nil {
		t.Fatalf("usable answer rejected: %v", err)
	}
	if len(envs) != 1 || envs[0] != "GOMODCACHE=/go/pkg/mod" {
		t.Fatalf("envs = %v, want the container path assigned to GOMODCACHE", envs)
	}

	// A directory that is not on the host is an empty mount: assigning it would
	// only teach the server that the project's dependencies do not exist.
	if _, err := applyDetectedValue(probe, "/go/pkg/nothing", mounts); err == nil {
		t.Error("a missing host directory was accepted")
	}

	// A probe with nothing to assign cannot produce an env.
	noEnvs := probe
	noEnvs.Envs = nil
	if _, err := applyDetectedValue(noEnvs, "/go/pkg/mod", mounts); err == nil {
		t.Error("a probe declaring no envs produced an assignment")
	}
}

// --- detectSessionEnvs: cached, per-workspace, never fatal ------------------

type fakeProber struct {
	calls       int
	byWorkspace map[string]string
	err         error
	lastArgv    []string
	lastRange   time.Duration
}

func (f *fakeProber) ExecInTaskSandbox(_ context.Context, workspacePath string, argv []string, timeout time.Duration) (string, error) {
	f.calls++
	f.lastArgv = argv
	f.lastRange = timeout
	if f.err != nil {
		return "", f.err
	}
	return f.byWorkspace[workspacePath], nil
}

func detectExt(t *testing.T) *extensions.Extension {
	t.Helper()
	return &extensions.Extension{
		ID:   "ext-gopls",
		Name: "gopls",
		SandboxConfig: extensions.SandboxConfig{
			Detect: []extensions.DetectProbe{{
				CommandRef: extensions.CommandRef{Command: "go", Args: []string{"env", "GOMODCACHE"}, TimeoutSeconds: 99},
				Envs:       []types.SandboxEnv{{Key: "GOMODCACHE", Value: "{value}"}},
			}},
		},
	}
}

func projStoreWith(t *testing.T, projects ...*types.Project) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, p := range projects {
		if err := s.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestDetectSessionEnvsResolvesPerWorkspaceAndCaches(t *testing.T) {
	root := t.TempDir()
	alpha, beta := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	for _, p := range []string{alpha, beta} {
		if err := os.MkdirAll(filepath.Join(p, "pkg", "mod"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alphaWS, betaWS := filepath.Join(alpha, "worktree"), filepath.Join(beta, "worktree")
	for _, ws := range []string{alphaWS, betaWS} {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projStore := projStoreWith(t,
		&types.Project{ID: "alpha", Name: "alpha", Path: alpha, SandboxEnabled: true},
		&types.Project{ID: "beta", Name: "beta", Path: beta, SandboxEnabled: true},
	)
	mgr := NewExtensionLSPManager(nil, nil, projStore)
	prober := &fakeProber{byWorkspace: map[string]string{
		alphaWS: "/go/pkg/mod\n", // the same container path in both sandboxes…
		betaWS:  "/go/pkg/mod\n",
	}}
	mgr.SetTaskProber(prober)

	// …but mirrored from a different host directory per project, which is what
	// makes the two answers different assignments in the end.
	alphaMounts := []types.SandboxMount{{HostPath: filepath.Join(alpha, "pkg", "mod"), ContainerPath: "/go/pkg/mod"}}
	betaMounts := []types.SandboxMount{{HostPath: filepath.Join(beta, "pkg", "mod"), ContainerPath: "/go/pkg/mod"}}

	got := mgr.detectSessionEnvs(context.Background(), detectExt(t), alphaWS, alphaMounts)
	if len(got) != 1 || got[0] != "GOMODCACHE=/go/pkg/mod" {
		t.Fatalf("alpha envs = %v", got)
	}
	if prober.calls != 1 {
		t.Fatalf("probe ran %d times, want 1", prober.calls)
	}
	// The manifest asked for 99 s; the ceiling is what a turn can afford.
	if prober.lastRange > detectMaxTimeout {
		t.Errorf("probe timeout %s exceeds the %s ceiling", prober.lastRange, detectMaxTimeout)
	}
	if strings.Join(prober.lastArgv, " ") != "go env GOMODCACHE" {
		t.Errorf("probe argv = %v", prober.lastArgv)
	}

	// A different project asks its own question — its cache is its own.
	if envs := mgr.detectSessionEnvs(context.Background(), detectExt(t), betaWS, betaMounts); len(envs) == 0 {
		t.Fatal("beta got no envs")
	}
	if prober.calls != 2 {
		t.Fatalf("probe ran %d times after a second project, want 2 (one per project)", prober.calls)
	}

	// A third call for alpha's project does not re-probe.
	mgr.detectSessionEnvs(context.Background(), detectExt(t), alphaWS, alphaMounts)
	if prober.calls != 2 {
		t.Fatalf("probe ran again for a cached project: %d calls", prober.calls)
	}
}

func TestDetectSessionEnvsNeverRunsOnTheHost(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plain")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	projStore := projStoreWith(t, &types.Project{ID: "plain", Name: "plain", Path: path, SandboxEnabled: false})
	mgr := NewExtensionLSPManager(nil, nil, projStore)
	prober := &fakeProber{byWorkspace: map[string]string{path: "/go/pkg/mod"}}
	mgr.SetTaskProber(prober)

	// A project with sandboxing disabled has no container to ask, and an
	// extension-declared command must never be run anywhere else.
	if envs := mgr.detectSessionEnvs(context.Background(), detectExt(t), path, nil); envs != nil {
		t.Fatalf("unsandboxed project produced %v", envs)
	}
	if prober.calls != 0 {
		t.Errorf("probe ran %d times for an unsandboxed project", prober.calls)
	}
}

func TestDetectSessionEnvsDegradesSilentlyAndBacksOff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(path, "mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := NewExtensionLSPManager(nil, nil, nil)
	prober := &fakeProber{err: errors.New("no running sandbox container")}
	mgr.SetTaskProber(prober)
	mounts := []types.SandboxMount{{HostPath: filepath.Join(path, "mod"), ContainerPath: "/go/pkg/mod"}}

	// A failed probe is "no detection", not an error to the turn.
	if envs := mgr.detectSessionEnvs(context.Background(), detectExt(t), path, mounts); envs != nil {
		t.Fatalf("failed probe produced %v", envs)
	}
	// …and it is remembered, so a cold container is not re-probed per file.
	mgr.detectSessionEnvs(context.Background(), detectExt(t), path, mounts)
	if prober.calls != 1 {
		t.Errorf("probe ran %d times, want 1 (failures are cached too)", prober.calls)
	}

	// An answer outside every configured mount is discarded: a path the server
	// cannot read is worse than no path at all.
	prober.err = nil
	prober.byWorkspace = map[string]string{path: "/somewhere/else"}
	delete(mgr.detections, detectEnvKey("", path, "ext-gopls"))
	if envs := mgr.detectSessionEnvs(context.Background(), detectExt(t), path, mounts); envs != nil {
		t.Fatalf("out-of-mount answer accepted: %v", envs)
	}

	// No prober wired at all: skip, do not fail.
	mgr2 := NewExtensionLSPManager(nil, nil, nil)
	if envs := mgr2.detectSessionEnvs(context.Background(), detectExt(t), path, mounts); envs != nil {
		t.Fatalf("unwired manager produced %v", envs)
	}
	// An extension that declares no probes costs nothing.
	if envs := mgr2.detectSessionEnvs(context.Background(), &extensions.Extension{ID: "x"}, path, mounts); envs != nil {
		t.Fatalf("probe-less extension produced %v", envs)
	}
}

// --- the environment check's command line -----------------------------------

func TestEnvCheckCommandQuotesAndSkipsUnusableKeys(t *testing.T) {
	gate := &extensions.EnvironmentGate{
		Check: extensions.CommandRef{Command: "go", Args: []string{"list", "-e", "-m", "all"}},
	}
	cmd, ok := envCheckCommand(gate, "/mnt/work tree", []string{
		"GOMODCACHE=/go/pkg/mod",
		"BROKEN=evil; rm -rf /",
		"not a key=x",
		"=novalue",
	})
	if !ok {
		t.Fatal("a declared check produced no command")
	}
	if !strings.HasPrefix(cmd, "cd '/mnt/work tree' && ") {
		t.Errorf("working directory not quoted as one word: %s", cmd)
	}
	if !strings.Contains(cmd, "GOMODCACHE='/go/pkg/mod'") {
		t.Errorf("session env not assigned to the command: %s", cmd)
	}
	for _, want := range []string{"'go'", "'list'", "'-e'", "'-m'", "'all'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command %q missing %q", cmd, want)
		}
	}
	// A value with shell metacharacters survives as data, never as syntax.
	if !strings.Contains(cmd, "BROKEN='evil; rm -rf /'") {
		t.Errorf("hostile value not carried as a quoted literal: %s", cmd)
	}
	// A key that is not a plain identifier is dropped, not passed to the shell.
	if strings.Contains(cmd, "not a key") || strings.Contains(cmd, "=novalue") {
		t.Errorf("unusable assignment reached the command line: %s", cmd)
	}
	// A value quote must stay inside its own word.
	tricky, _ := envCheckCommand(gate, "", []string{"PATHISH=/a'b/c"})
	if !strings.Contains(tricky, `'/a'\''b/c'`) {
		t.Errorf("quote not escaped: %s", tricky)
	}

	if _, ok := envCheckCommand(&extensions.EnvironmentGate{}, "", nil); ok {
		t.Error("a check with no command produced a command line")
	}
}

func TestIsEnvKey(t *testing.T) {
	for _, good := range []string{"GOMODCACHE", "GOCACHE", "XDG_CACHE_HOME", "A1"} {
		if !isEnvKey(good) {
			t.Errorf("%q rejected", good)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=B", "A;B", "PATH(1)"} {
		if isEnvKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// --- reload_files ------------------------------------------------------------

func TestReloadNeeded(t *testing.T) {
	declared := []string{"go.mod", "go.sum"}
	yes := []string{"go.mod", "go.sum", "internal/foo/go.mod", "./go.mod", " go.mod "}
	no := []string{"main.go", "go.work", "", "  ", ".", "go.mod.bak", "cmd/go/routes.go"}
	for _, f := range yes {
		if !reloadNeeded(declared, f) {
			t.Errorf("%q should trigger a reload", f)
		}
	}
	for _, f := range no {
		if reloadNeeded(declared, f) {
			t.Errorf("%q should not trigger a reload", f)
		}
	}
	// An extension that declares nothing never reloads.
	if reloadNeeded(nil, "go.mod") {
		t.Error("a manifest without reload_files recycled a session")
	}
}

func TestEnvironmentReportSuppressesOnlyWhatItDeclares(t *testing.T) {
	report := EnvironmentReport{Healthy: false, SuppressCodes: []string{"BrokenImport", "go list"}}
	if !report.Suppresses("BrokenImport") || !report.Suppresses("go list") {
		t.Error("declared codes not suppressed")
	}
	if report.Suppresses("syntax") || report.Suppresses("") {
		t.Error("undeclared code suppressed")
	}
	// Healthy means report everything, whatever the extension declared.
	healthy := EnvironmentReport{Healthy: true, SuppressCodes: []string{"BrokenImport"}}
	if healthy.Suppresses("nope") {
		t.Error("only codes in the list are ever suppressed")
	}
}
