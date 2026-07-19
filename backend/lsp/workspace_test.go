package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectWorkspaceRootFindsGoModTwoLevelsUp(t *testing.T) {
	defer ResetWorkspaceCache()

	tmp := t.TempDir()
	// Structure: tmp/project/pkg/mymodule/main.go
	// go.mod at tmp/project/go.mod
	nested := filepath.Join(tmp, "project", "pkg", "mymodule")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := filepath.Join(tmp, "project", "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(nested, "main.go")

	root := DetectWorkspaceRoot(mainGo, tmp)
	expected := filepath.Join(tmp, "project")
	if root != expected {
		t.Errorf("expected root %s, got %s", expected, root)
	}
}

func TestDetectWorkspaceRootFallsBackToProjectPath(t *testing.T) {
	defer ResetWorkspaceCache()

	tmp := t.TempDir()
	// Structure: tmp/project/src/main.go — no go.mod anywhere
	src := filepath.Join(tmp, "project", "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(src, "main.go")

	root := DetectWorkspaceRoot(mainGo, tmp)
	if root != tmp {
		t.Errorf("expected fallback to project path %s, got %s", tmp, root)
	}
}

func TestDetectWorkspaceRootStopsAtProjectBoundary(t *testing.T) {
	defer ResetWorkspaceCache()

	// Create an outer directory with a go.mod that is ABOVE the project root
	outer := t.TempDir()
	outerGoMod := filepath.Join(outer, "go.mod")
	if err := os.WriteFile(outerGoMod, []byte("module outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Project is a subdirectory — the outer go.mod should NOT be found
	projectPath := filepath.Join(outer, "myproject")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(projectPath, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(src, "main.go")

	root := DetectWorkspaceRoot(mainGo, projectPath)
	if root != projectPath {
		t.Errorf("expected root to stay at project boundary %s, got %s", projectPath, root)
	}
}

func TestDetectWorkspaceRootCaching(t *testing.T) {
	defer ResetWorkspaceCache()

	tmp := t.TempDir()
	// Structure: tmp/project/pkg/main.go with go.mod at tmp/project/go.mod
	pkg := filepath.Join(tmp, "project", "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := filepath.Join(tmp, "project", "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(pkg, "main.go")

	// First call — should walk the filesystem
	root1 := DetectWorkspaceRoot(mainGo, tmp)
	expected := filepath.Join(tmp, "project")
	if root1 != expected {
		t.Errorf("first call: expected root %s, got %s", expected, root1)
	}

	// Second call — should come from cache (same projectPath)
	root2 := DetectWorkspaceRoot(mainGo, tmp)
	if root2 != expected {
		t.Errorf("second call: expected root %s, got %s", expected, root2)
	}

	// Verify cache was populated
	workspaceCache.Lock()
	cached, ok := workspaceCache.m[tmp]
	workspaceCache.Unlock()
	if !ok {
		t.Error("expected project path to be in cache")
	}
	if cached != expected {
		t.Errorf("cache value: expected %s, got %s", expected, cached)
	}
}

func TestDetectWorkspaceRootFindsGoModAtProjectRoot(t *testing.T) {
	defer ResetWorkspaceCache()

	tmp := t.TempDir()
	// go.mod is exactly at projectPath
	goMod := filepath.Join(tmp, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/baz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(src, "main.go")

	root := DetectWorkspaceRoot(mainGo, tmp)
	if root != tmp {
		t.Errorf("expected root %s, got %s", tmp, root)
	}
}

func TestDetectWorkspaceRootFindsClosestGoMod(t *testing.T) {
	defer ResetWorkspaceCache()

	// Create nested modules: outer/go.mod and inner/go.mod
	// File is in inner/ — should find inner/go.mod, not outer/go.mod
	tmp := t.TempDir()
	outerMod := filepath.Join(tmp, "outer", "go.mod")
	innerDir := filepath.Join(tmp, "outer", "inner")
	innerMod := filepath.Join(innerDir, "go.mod")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outerMod, []byte("module outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(innerMod, []byte("module inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(innerDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(src, "main.go")

	root := DetectWorkspaceRoot(mainGo, tmp)
	expected := innerDir
	if root != expected {
		t.Errorf("expected closest go.mod at %s, got %s", expected, root)
	}
}
