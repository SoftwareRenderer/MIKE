package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/sandboxcheck"
	"mike/pkg/sandboxruntime"
)

// These pin what the build path must control - the context it creates and what is staged in it - not
// whether buildah can build, which the gated test below covers against a real daemon.

// One build must hand podman a named context and nothing else, with the bridge inside it.
//
// The argv is asserted as the bytes the fake podman was exec'd with, so it pins what reached the
// subprocess rather than what a helper recorded: exactly `build -t <ref> -f <ctx>/Dockerfile <ctx>`.
// A bare "-" would take the Dockerfile from stdin and a "." the context from the working directory;
// neither can appear in it. A .dockerignore is planted below the working directory because an ignore
// file written for another build decides what a context it governs holds — the context is created by
// this package under the temp root, where that file has nothing to say about anything.
func TestBuildExtensionImageNamesItsOwnContextAndStagesTheBridge(t *testing.T) {
	cwd := t.TempDir()
	// No bridge anywhere near the ignore file, so the context can only hold
	// sandbox/sandbox-bridge by being staged.
	if err := os.WriteFile(filepath.Join(cwd, ".dockerignore"), []byte("sandbox/\nsandbox/sandbox-bridge\n*"), 0o644); err != nil {
		t.Fatalf("write .dockerignore: %v", err)
	}
	t.Chdir(cwd)

	fake := fakePodmanOK(t)
	rc := NewRegistryClientWithBoot(bridgeBoot(t))

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
	if ctx.Dir == "" {
		t.Fatal("no context directory was recorded")
	}
	want := []string{"build", "-t", ref, "-f", filepath.Join(ctx.Dir, "Dockerfile"), ctx.Dir}
	if fmt.Sprint(calls[0]) != fmt.Sprint(want) {
		t.Errorf("argv = %v, want %v", calls[0], want)
	}
	for _, call := range calls {
		for _, a := range call {
			if a == "." || a == "-" || strings.HasPrefix(a, cwd) {
				t.Errorf("argv %v reaches the working directory (%q)", call, a)
			}
		}
	}
	if sameDir(t, ctx.Dir, workingDir(t)) {
		t.Errorf("the context is the working directory (%s)", ctx.Dir)
	}
	if strings.HasPrefix(ctx.Dir, cwd) {
		t.Errorf("the build context %s lives under the working directory %s, so its .dockerignore governs it", ctx.Dir, cwd)
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
	// Two files, no more: what the build needs, not what the tree holds.
	if len(ctx.Files) != 2 {
		t.Errorf("context holds %v, want exactly the Dockerfile and the bridge", ctx.Files)
	}
}

// The same claim against real buildah. Gated on this host being able to build an image at all (the
// graph driver and its mount program, which a container-start probe does not exercise) and on the
// base it fetches.
func TestBuildExtensionImage_RealBuildSurvivesADockerignoreBelow(t *testing.T) {
	sandboxcheck.RequireBuild(t)
	sandboxcheck.Require(t, "docker.io/library/alpine:latest")
	rt := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{})
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".dockerignore"), []byte("sandbox/sandbox-bridge\n"), 0o644); err != nil {
		t.Fatalf("write .dockerignore: %v", err)
	}
	t.Chdir(cwd)
	rc := NewRegistryClientWithBoot(bridgeBoot(t))
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
	// The wired path names a missing file, and no bridge sits under the test's working directory.
	rc := NewRegistryClientWithBoot(sandboxruntime.Boot{AgentPath: filepath.Join(t.TempDir(), "not-a-bridge")})

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
	rc := NewRegistryClientWithBoot(bridgeBoot(t))

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
	rc := NewRegistryClientWithBoot(bridgeBoot(t))

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
