package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/prompts"
	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

// TestProjectOptsCarriesTheProjectSkillDirectory: the scanned directory and the bound one are one
// project row's decision.
func TestProjectOptsCarriesTheProjectSkillDirectory(t *testing.T) {
	projectDir := t.TempDir()
	project := &types.Project{ID: "p-skills", Name: "skills-proj", Path: projectDir}

	opts := ProjectOpts(project)
	if want := prompts.ProjectSkillsDir(projectDir); opts.ProjectSkillsDir != want {
		t.Errorf("ProjectSkillsDir = %q, want the directory the prompt scans, %q", opts.ProjectSkillsDir, want)
	}
	if got := ProjectOpts(&types.Project{ID: "p-none"}).ProjectSkillsDir; got != "" {
		t.Errorf("a project with no directory asked for %q, want nothing bound", got)
	}
}

// TestReadOnlyBindArgBindsOnlyWhatExists: both skills mounts are read-only, and a directory that was
// never created binds nothing rather than refusing the container.
func TestReadOnlyBindArgBindsOnlyWhatExists(t *testing.T) {
	dir := t.TempDir()
	skills := prompts.ProjectSkillsDir(dir)
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}

	got := readOnlyBindArg("task-1", "project skills", skills, ProjectSkillsMount)
	want := []string{"-v", sandboxruntime.BindMountSpec(skills, ProjectSkillsMount, "ro")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("args = %v, want %v (the mount is read-only and at the container path the prompt names)", got, want)
	}

	for name, hostDir := range map[string]string{
		"unset":   "",
		"missing": filepath.Join(dir, "never-created"),
	} {
		if args := readOnlyBindArg("task-1", name, hostDir, ProjectSkillsMount); args != nil {
			t.Errorf("%s directory produced %v, want no mount at all", name, args)
		}
	}
}
