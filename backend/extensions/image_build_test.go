package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/pkg/sandboxruntime"
)

// These pin what the build path must control - the context it creates and what is staged in it - not
// whether buildah can build, which the gated test below covers against a real daemon.

func TestBuildExtensionImageArgs(t *testing.T) {
	got := buildExtensionImageArgs("localhost/kanban-ext-gopls:0.7.0", "/tmp/ctx")
	want := []string{"build", "-t", "localhost/kanban-ext-gopls:0.7.0", "-f", filepath.Join("/tmp/ctx", "Dockerfile"), "/tmp/ctx"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// Reading the Dockerfile from stdin with the CWD as context is what let a .dockerignore written for
// another build decide what an extension build could see.
func TestBuildExtensionImageArgs_NeverReadsStdinOrTheWorkingDirectory(t *testing.T) {
	args := buildExtensionImageArgs("localhost/kanban-ext-x:1.0.0", "/tmp/ctx")
	for i, a := range args {
		if a == "-" {
			t.Errorf("args[%d] is a bare \"-\": the Dockerfile must come from the context, not stdin (args: %v)", i, args)
		}
		if a == "." {
			t.Errorf("args[%d] is \".\": the context must be named, never inherited from the CWD (args: %v)", i, args)
		}
		if i > 0 && args[i-1] == "-f" && a == "-" {
			t.Errorf("-f takes stdin: %v", args)
		}
	}
}

func TestBuildExtensionImage_StagesTheBridgeIntoTheContext(t *testing.T) {
	fake := fakePodmanOK(t)
	setBridgeEnv(t)
	rc := NewRegistryClient()

	const ref = "localhost/kanban-ext-gopls:0.7.0"
	dockerfile := "FROM docker.io/library/alpine:latest\nCOPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n"
	if err := rc.BuildExtensionImage(dockerfile, ref); err != nil {
		t.Fatalf("BuildExtensionImage: %v", err)
	}

	calls := fake.calls(t)
	if len(calls) != 1 {
		t.Fatalf("podman called %d times, want 1: %v", len(calls), calls)
	}
	ctx := fake.context(t, 0)
	want := []string{"build", "-t", ref, "-f", filepath.Join(ctx.Dir, "Dockerfile"), ctx.Dir}
	if fmt.Sprint(calls[0]) != fmt.Sprint(want) {
		t.Errorf("argv = %v, want %v", calls[0], want)
	}

	found := map[string]bool{}
	for _, f := range ctx.Files {
		found[filepath.ToSlash(f)] = true
	}
	if !found["Dockerfile"] {
		t.Errorf("context %v holds no Dockerfile: %v", ctx.Dir, ctx.Files)
	}
	// The path extension Dockerfiles copy; a different layout builds an image with no bridge in it.
	if !found[extensionBuildContextBridge] {
		t.Errorf("context holds no %s: %v — COPY sandbox/sandbox-bridge would fail", extensionBuildContextBridge, ctx.Files)
	}
	if !ctx.BridgeIsExecutable {
		t.Errorf("the staged %s is not executable; a Dockerfile COPY preserves the mode", extensionBuildContextBridge)
	}
	if ctx.HasDockerignore {
		t.Error("the build context carries a .dockerignore: it is what filtered the bridge out last time")
	}
	if ctx.Dir == "" {
		t.Fatal("no context directory was recorded")
	}
	if sameDir(t, ctx.Dir, workingDir(t)) {
		t.Errorf("the context is the working directory (%s)", ctx.Dir)
	}
	// Two files, no more: what the build needs, not what the tree holds.
	if len(ctx.Files) != 2 {
		t.Errorf("context holds %v, want exactly the Dockerfile and the bridge", ctx.Files)
	}
}

// The bug's shape: a .dockerignore in the working directory that excludes the bridge must not be able
// to starve an extension build.
func TestBuildExtensionImage_IgnoresADockerignoreInTheWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	// The ignore file from the bug, and no bridge anywhere near it: the only way the context can hold
	// sandbox/sandbox-bridge is by being staged.
	if err := os.WriteFile(filepath.Join(cwd, ".dockerignore"), []byte("sandbox/\nsandbox/sandbox-bridge\n*"), 0o644); err != nil {
		t.Fatalf("write .dockerignore: %v", err)
	}
	t.Chdir(cwd)

	fake := fakePodmanOK(t)
	setBridgeEnv(t)
	rc := NewRegistryClient()
	if err := rc.BuildExtensionImage("FROM docker.io/library/alpine:latest\nCOPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n", "localhost/kanban-ext-gopls:0.7.0"); err != nil {
		t.Fatalf("BuildExtensionImage: %v", err)
	}

	ctx := fake.context(t, 0)
	if ctx.Dir == "" {
		t.Fatal("no context directory was recorded")
	}
	if strings.HasPrefix(ctx.Dir, cwd) {
		t.Errorf("the build context %s lives under the working directory %s, so its .dockerignore governs it", ctx.Dir, cwd)
	}
	for _, call := range fake.calls(t) {
		for _, a := range call {
			if a == "." || a == "-" || strings.HasPrefix(a, cwd) {
				t.Errorf("argv %v reaches the working directory (%q)", call, a)
			}
		}
	}
	var staged bool
	for _, f := range ctx.Files {
		if filepath.ToSlash(f) == extensionBuildContextBridge {
			staged = true
		}
	}
	if !staged {
		t.Errorf("context %v does not hold %s: %v — the build would fail with the file filtered out of the context",
			ctx.Dir, extensionBuildContextBridge, ctx.Files)
	}
}

// The same claim against real buildah: the only test here that shows the fix works rather than that the
// assertion changed. Gated on a local base image - it never reaches the network.
func TestBuildExtensionImage_RealBuildSurvivesADockerignoreBelow(t *testing.T) {
	rt := sandboxruntime.NewRuntime()
	if testing.Short() || !rt.ImageExists("docker.io/library/alpine:latest") {
		t.Skip("needs a local alpine and a working podman/buildah")
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".dockerignore"), []byte("sandbox/sandbox-bridge\n"), 0o644); err != nil {
		t.Fatalf("write .dockerignore: %v", err)
	}
	t.Chdir(cwd)
	setBridgeEnv(t)

	rc := NewRegistryClient()
	ref := "localhost/kanban-ext-build-test:1"
	t.Cleanup(func() { _, _ = rt.PodmanExec("rmi", "-f", ref) })

	// If the context were the working directory, this COPY is the step that failed.
	dockerfile := "FROM docker.io/library/alpine:latest\n" +
		"COPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n" +
		"RUN test -x /usr/local/bin/sandbox-bridge\n"
	if err := rc.BuildExtensionImage(dockerfile, ref); err != nil {
		t.Fatalf("BuildExtensionImage against a filtered working directory: %v", err)
	}
}

func TestBuildExtensionImage_RefusesWithoutABridge(t *testing.T) {
	fake := fakePodmanOK(t)
	// MIKE_AGENT_PATH unset to a missing file, with no bridge beside the test binary or in the CWD.
	t.Setenv("MIKE_AGENT_PATH", filepath.Join(t.TempDir(), "not-a-bridge"))
	if _, err := os.Stat("sandbox/sandbox-bridge"); err == nil {
		t.Skip("a sandbox-bridge sits under the working directory; nothing to refuse")
	}

	rc := NewRegistryClient()
	err := rc.BuildExtensionImage("FROM docker.io/library/alpine:latest\n", "localhost/kanban-ext-gopls:0.7.0")
	if err == nil {
		t.Fatal("BuildExtensionImage with no bridge = nil, want an error naming sandbox-bridge")
	}
	if !strings.Contains(err.Error(), "sandbox-bridge") {
		t.Errorf("error = %v, want it to name sandbox-bridge", err)
	}
	if calls := fake.calls(t); len(calls) != 0 {
		t.Errorf("podman was called %v; a doomed build must not be started", calls)
	}
}

func TestBuildExtensionImage_FailureCarriesPodmanOutput(t *testing.T) {
	fake := fakePodmanFails(t, 1, `Error: building at STEP "COPY": no items matching glob`)
	setBridgeEnv(t)
	rc := NewRegistryClient()

	err := rc.BuildExtensionImage("FROM docker.io/library/alpine:latest\n", "localhost/kanban-ext-gopls:0.7.0")
	if err == nil {
		t.Fatal("BuildExtensionImage with a failing podman = nil, want error")
	}
	if !strings.Contains(err.Error(), "no items matching glob") {
		t.Errorf("error = %v, want podman's own words in it", err)
	}
	if !strings.Contains(err.Error(), "localhost/kanban-ext-gopls:0.7.0") {
		t.Errorf("error = %v, want it to name the image being built", err)
	}
	if len(fake.calls(t)) != 1 {
		t.Errorf("podman calls = %v, want exactly one", fake.calls(t))
	}
}

func TestBuildExtensionImage_RejectsAnEmptyDockerfileAndABadRef(t *testing.T) {
	fake := fakePodmanOK(t)
	setBridgeEnv(t)
	rc := NewRegistryClient()

	if err := rc.BuildExtensionImage("  \n\t\n", "localhost/kanban-ext-gopls:0.7.0"); err == nil {
		t.Error("BuildExtensionImage(empty Dockerfile) = nil, want an error")
	}
	if err := rc.BuildExtensionImage("FROM alpine", "-t /etc/passwd"); err == nil {
		t.Error("BuildExtensionImage(dash-prefixed ref) = nil, want the ref refused before podman")
	}
	if calls := fake.calls(t); len(calls) != 0 {
		t.Errorf("podman was called %v for inputs that must be refused up front", calls)
	}
}

func workingDir(t *testing.T) string {
	t.Helper()
	p, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return p
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
