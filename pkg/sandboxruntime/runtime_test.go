package sandboxruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		project string
		wantErr bool
		errMsg  string
	}{
		{"simple name", "my-project", false, ""},
		{"with spaces", "My Project Name", false, ""},
		{"with underscores", "my_project_v2", false, ""},
		{"with dots", "my.project.name", false, ""},
		{"with slashes", "my/project", false, ""},
		{"mixed valid chars", "My.Project_Name-2.0", false, ""},
		{"single char", "a", false, ""},
		{"numeric", "project123", false, ""},
		{"empty string", "", true, "must not be empty"},
		{"leading dash", "-my-project", true, "must not start with a dash"},
		{"double dash", "--verbose", true, "must not start with a dash"},
		{"dotdot whole name", "..", true, "'..' path segments"},
		{"dotdot escape", "a/../../b", true, "'..' path segments"},
		{"dotdot trailing", "my/..", true, "'..' path segments"},
		{"dotdot leading", "/..", true, "'..' path segments"},
		{"dots inside segment", "my..project", false, ""},
		{"single dot segment", "my/./project", false, ""},
		{"control char newline", "my\nproject", true, "control characters"},
		{"control char tab", "my\tproject", true, "control characters"},
		{"control char null", "my\x00project", true, "control characters"},
		{"control char del", "my\x7fproject", true, "control characters"},
		{"shell injection", "my; rm -rf /", true, "invalid character"},
		{"shell injection pipe", "my | cat", true, "invalid character"},
		{"shell injection backtick", "my`whoami`project", true, "invalid character"},
		{"shell injection dollar", "my$(rm /)project", true, "invalid character"},
		{"parentheses", "my(project)", true, "invalid character"},
		{"angle brackets", "my<project>", true, "invalid character"},
		{"ampersand", "my&project", true, "invalid character"},
		{"exclamation", "my!project", true, "invalid character"},
		{"question mark", "my?project", true, "invalid character"},
		{"asterisk", "my*project", true, "invalid character"},
		{"hash", "my#project", true, "invalid character"},
		{"percent", "my%project", true, "invalid character"},
		{"caret", "my^project", true, "invalid character"},
		{"backslash", "my\\project", true, "invalid character"},
		{"quotes", `my"project"`, true, "invalid character"},
		{"single quote", "my'project", true, "invalid character"},
		{"equals", "my=project", true, "invalid character"},
		{"plus", "my+project", true, "invalid character"},
		{"comma", "my,project", true, "invalid character"},
		{"colon", "my:project", true, "invalid character"},
		{"semicolon", "my;project", true, "invalid character"},
		{"at sign", "my@project", true, "invalid character"},
		{"very long name", "a" + strings.Repeat("b", 256), true, "exceed 256"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectName(tt.project)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProjectName(%q) error = %v, wantErr %v", tt.project, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateProjectName(%q) error = %q, want error containing %q", tt.project, err.Error(), tt.errMsg)
			}
		})
	}
}

func TestValidateImageRef(t *testing.T) {
	r := NewRuntime()
	tests := []struct {
		name    string
		image   string
		wantErr bool
	}{
		{"valid ubuntu", "docker.io/library/ubuntu:latest", false},
		{"valid golang", "golang:1.21", false},
		{"valid with digest", "ubuntu@sha256:abc123", false},
		{"valid localhost", "localhost:5000/myimage:latest", false},
		{"valid kanban prefix", "kanban-ext-gopls:1.0.0", false},
		{"valid project image", "sandbox-my-project", false},
		{"valid simple", "alpine", false},
		{"dash prefix", "-v", true},
		{"dash prefix long", "--format=json", true},
		{"dash prefix space", "-t something", true},
		{"empty string", "", true},
		{"space injection", "ubuntu:latest -v", true},
		{"shell injection", "$(rm -rf /)", true},
		{"semicolon injection", "ubuntu; rm -rf /", true},
		{"pipe injection", "ubuntu | cat", true},
		{"backtick injection", "ubun`whoami`t", true},
		{"newline injection", "ubuntu\nrm", true},
		{"tab character", "ubun\ttu", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.ValidateImageRef(tt.image)
			if (err != nil) != tt.wantErr {
				t.Errorf("r.ValidateImageRef(%q) error = %v, wantErr %v", tt.image, err, tt.wantErr)
			}
		})
	}
}

func TestParseUnifiedCgroupPath(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
		wantErr  bool
	}{
		{
			name:     "single v2 line",
			contents: "0::/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-conmon-abc.scope\n",
			want:     "/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-conmon-abc.scope",
		},
		{
			name:     "multi-line with v1 controllers",
			contents: "12:memory:/\n11:cpu,cpuacct:/\n0::/system.slice/docker.service\n",
			want:     "/system.slice/docker.service",
		},
		{
			name:     "v2 root",
			contents: "0::\n",
			want:     "/",
		},
		{
			name:     "v1 only",
			contents: "12:memory:/\n11:cpu,cpuacct:/\n",
			wantErr:  true,
		},
		{
			name:     "empty",
			contents: "",
			wantErr:  true,
		},
		{
			name:     "whitespace only",
			contents: " \n\t\n",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUnifiedCgroupPath(tt.contents)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseUnifiedCgroupPath(%q) error = %v, wantErr %v", tt.contents, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseUnifiedCgroupPath(%q) = %q, want %q", tt.contents, got, tt.want)
			}
		})
	}
}

func TestReadCgroupMemory(t *testing.T) {
	const (
		mb = int64(1024 * 1024)
	)
	tests := []struct {
		name    string
		files   map[string]string
		want    CgroupMemory
		wantErr bool
	}{
		{
			name: "full cgroup with limit and oom kills",
			files: map[string]string{
				"memory.current": "104857600\n", // 100 MB
				"memory.peak":    "209715200\n", // 200 MB
				"memory.max":     "536870912\n", // 512 MB
				"memory.events":  "high 0\nmax 1\nlow 0\noom 0\noom_kill 3\n",
			},
			want: CgroupMemory{CurrentBytes: 100 * mb, PeakBytes: 200 * mb, MaxBytes: 512 * mb, OOMKills: 3},
		},
		{
			// "max" (unlimited) behaves like an absent limit: MaxBytes stays 0.
			name: "unlimited memory.max",
			files: map[string]string{
				"memory.current": "4194304\n", // 4 MB
				"memory.max":     "max\n",
			},
			want: CgroupMemory{CurrentBytes: 4 * mb},
		},
		{
			name: "missing memory.peak is tolerated",
			files: map[string]string{
				"memory.current": "1048576\n", // 1 MB
				"memory.max":     "1073741824\n",
			},
			want: CgroupMemory{CurrentBytes: 1 * mb, MaxBytes: 1024 * mb},
		},
		{
			name: "missing memory.max and memory.events are tolerated",
			files: map[string]string{
				"memory.current": "1048576\n",
			},
			want: CgroupMemory{CurrentBytes: 1 * mb},
		},
		{
			name: "events without oom_kill line",
			files: map[string]string{
				"memory.current": "1048576\n",
				"memory.events":  "high 0\nmax 0\nlow 0\noom 0\n",
			},
			want: CgroupMemory{CurrentBytes: 1 * mb},
		},
		{
			name:    "missing memory.current",
			files:   map[string]string{},
			wantErr: true,
		},
		{
			name: "malformed memory.current",
			files: map[string]string{
				"memory.current": "garbage\n",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			got, err := ReadCgroupMemory(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadCgroupMemory() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ReadCgroupMemory() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadCgroupMemory_MissingDir(t *testing.T) {
	if _, err := ReadCgroupMemory(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Error("expected error for missing cgroup directory")
	}
}

func TestClearCgroupMemoryLimit(t *testing.T) {
	dir := t.TempDir()
	// Simulate a cgroup with a 512 MB limit and no swap.
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("536870912"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("1048576"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := clearCgroupMemoryLimit(dir); err != nil {
		t.Fatalf("clearCgroupMemoryLimit() = %v", err)
	}

	for _, f := range []string{"memory.max", "memory.swap.max"} {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if string(data) != "max" {
			t.Errorf("%s = %q, want max", f, data)
		}
	}

	// The cleared cgroup reads back as unlimited.
	mem, err := ReadCgroupMemory(dir)
	if err != nil {
		t.Fatalf("ReadCgroupMemory() = %v", err)
	}
	if mem.MaxBytes != 0 {
		t.Errorf("MaxBytes = %d, want 0 (unlimited)", mem.MaxBytes)
	}
}

func TestClearCgroupMemoryLimit_MissingDir(t *testing.T) {
	if err := clearCgroupMemoryLimit(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Error("expected error for missing cgroup directory")
	}
}

func TestValidateDockerfilePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid absolute", "/home/user/Dockerfile", false},
		{"valid relative", "Dockerfile", false},
		{"valid subpath", "path/to/Dockerfile", false},
		{"dash prefix", "-f", true},
		{"dash prefix path", "--file=/tmp/Dockerfile", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDockerfilePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDockerfilePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

// TestGVisorAvailable_FalseWithoutRunsc pins the fail-closed path: with a PATH
// that has no runsc, the probe must report unavailable rather than assuming it.
// Both sandbox managers now share this probe, so a false here means neither will
// attempt a gVisor container.
func TestGVisorAvailable_FalseWithoutRunsc(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)

	if GVisorAvailable("test") {
		t.Error("GVisorAvailable = true with an empty PATH, want false")
	}
}

// TestGVisorAvailable_FalseWhenVersionCheckFails covers the second gate: runsc
// present but unusable. This is the check the extension sandbox manager used to
// skip, so the two managers could disagree about a broken install.
func TestGVisorAvailable_FalseWhenVersionCheckFails(t *testing.T) {
	bin := t.TempDir()
	// A runsc that exists but always fails its --version call.
	writeStub(t, filepath.Join(bin, "runsc"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", bin)

	if GVisorAvailable("test") {
		t.Error("GVisorAvailable = true when `runsc --version` fails, want false")
	}
}

// TestGVisorAvailable_TrueWithRunscAndPodman pins the success path, including the
// requirement that podman is present too — runsc alone is not enough.
func TestGVisorAvailable_TrueWithRunscAndPodman(t *testing.T) {
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "runsc"), "#!/bin/sh\necho 'runsc version test'\n")
	writeStub(t, filepath.Join(bin, "podman"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)

	if !GVisorAvailable("test") {
		t.Error("GVisorAvailable = false with stub runsc+podman, want true")
	}
}

// TestGVisorAvailable_FalseWithoutPodman: runsc healthy, podman missing.
func TestGVisorAvailable_FalseWithoutPodman(t *testing.T) {
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "runsc"), "#!/bin/sh\necho 'runsc version test'\n")
	t.Setenv("PATH", bin)

	if GVisorAvailable("test") {
		t.Error("GVisorAvailable = true with no podman, want false")
	}
}

// TestCleanupStaleContainers_UnreachablePodmanIsBestEffort: the startup sweep
// must swallow a broken podman (returns nothing, does not panic) rather than
// block or crash server startup.
func TestCleanupStaleContainers_UnreachablePodmanIsBestEffort(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no podman on PATH

	rt := NewRuntime()
	if got := rt.CleanupStaleContainers("test", "kanban-sandbox-"); got != nil {
		t.Errorf("CleanupStaleContainers = %v with no podman, want nil", got)
	}
}

// writeStub creates an executable script at path with the given contents.
func writeStub(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
}

// TestStaleContainerNamesLeavesRunningContainersAlone: the startup sweep matches a whole name
// family by prefix, so a running container it removes belongs to somebody else who is still using
// it — a second server on the host, or a unit test that builds a sandbox manager while a real
// server is serving language sessions. The name the sweep wants back is only ever held by a
// leftover, and Create removes a same-named leftover itself.
func TestStaleContainerNamesLeavesRunningContainersAlone(t *testing.T) {
	running := map[string]bool{
		"kanban-ext-gopls-live":     true,
		"kanban-sandbox-task-live":  true,
		"kanban-ext-gopls-leftover": false,
	}
	isRunning := func(name string) (bool, error) {
		if name == "kanban-ext-uninspectable" {
			return false, os.ErrPermission
		}
		return running[name], nil
	}

	got := staleContainerNames([]string{
		"kanban-ext-gopls-live",
		"kanban-sandbox-task-live",
		"kanban-ext-gopls-leftover",
		"kanban-ext-uninspectable",
	}, isRunning)

	want := map[string]bool{"kanban-ext-gopls-leftover": true, "kanban-ext-uninspectable": true}
	if len(got) != len(want) {
		t.Fatalf("sweep list = %v, want exactly %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("sweep list contains %q (running containers must never be swept)", name)
		}
	}
}

// TestStaleContainerNamesKeepsEverythingWhenStateIsUnreadable: a podman that cannot answer the
// state query is exactly the half-dead state a leftover is in, so the sweep must not lose its one
// chance to clear the name.
func TestStaleContainerNamesKeepsEverythingWhenStateIsUnreadable(t *testing.T) {
	isRunning := func(string) (bool, error) { return false, os.ErrClosed }
	got := staleContainerNames([]string{"kanban-sandbox-a", "kanban-sandbox-b"}, isRunning)
	if len(got) != 2 {
		t.Errorf("sweep list = %v, want both containers", got)
	}
}

// TestSkipStartupCleanupIsTheSharedSwitch: the extension sandbox manager has to honour the same
// switch the task sandbox manager does, or a test binary that opts out of one sweep still wipes
// the other family's containers.
func TestSkipStartupCleanupIsTheSharedSwitch(t *testing.T) {
	t.Setenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP", "")
	if SkipStartupCleanup() {
		t.Error("a blank value must mean \"not set\" — same rule the task sandbox manager has always used")
	}
	t.Setenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP", "1")
	if !SkipStartupCleanup() {
		t.Error("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP=1 must skip the sweep")
	}
}
