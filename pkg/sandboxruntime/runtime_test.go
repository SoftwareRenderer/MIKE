package sandboxruntime

import (
	"os"
	"path/filepath"
	"strconv"
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
		{"name is a dot", ".", true, "names no directory"},
		{"name is a dot and a slash", "./", true, "names no directory"},
		{"name is a slash", "/", true, "names no directory"},
		{"name is only slashes", "///", true, "names no directory"},
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
			err := ValidateImageRef(tt.image)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateImageRef(%q) error = %v, wantErr %v", tt.image, err, tt.wantErr)
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
			name: "anon, guest RAM, limit and oom kills",
			files: map[string]string{
				"memory.stat":   "anon 104857600\nfile 157286400\nshmem 52428800\nkernel 4194304\n",
				"memory.max":    "536870912\n",
				"memory.events": "high 0\nmax 1\nlow 0\noom 0\noom_kill 3\n",
			},
			// 100 MB of host heaps plus 50 MB of guest RAM, under a 512 MB limit.
			want: CgroupMemory{AnonBytes: 100 * mb, ShmemBytes: 50 * mb, MaxBytes: 512 * mb, OOMKills: 3},
		},
		{
			// cgroup v2 counts shmem as part of file, so the file line says nothing about held memory.
			name: "host page cache does not enter what the sandbox holds",
			files: map[string]string{
				"memory.stat": "anon " + strconv.FormatInt(mb, 10) +
					"\nfile " + strconv.FormatInt(251*mb, 10) +
					"\nshmem " + strconv.FormatInt(200*mb, 10) + "\n",
			},
			want: CgroupMemory{AnonBytes: 1 * mb, ShmemBytes: 200 * mb},
		},
		{
			// "max" (unlimited) behaves like an absent limit: MaxBytes stays 0.
			name: "unlimited memory.max",
			files: map[string]string{
				"memory.stat": "anon 4194304\n", // 4 MB
				"memory.max":  "max\n",
			},
			want: CgroupMemory{AnonBytes: 4 * mb},
		},
		{
			name: "missing memory.max and memory.events are tolerated",
			files: map[string]string{
				"memory.stat": "anon 1048576\n",
			},
			want: CgroupMemory{AnonBytes: 1 * mb},
		},
		{
			name: "events without oom_kill line",
			files: map[string]string{
				"memory.stat":   "anon 1048576\n",
				"memory.events": "high 0\nmax 0\nlow 0\noom 0\n",
			},
			want: CgroupMemory{AnonBytes: 1 * mb},
		},
		{
			// Every caller's number rests on anon, so its absence is no data rather than 0 MB.
			name: "memory.stat without an anon line",
			files: map[string]string{
				"memory.stat": "file 1048576\nslab 4096\n",
			},
			wantErr: true,
		},
		{
			name:    "missing memory.stat",
			files:   map[string]string{"memory.max": "1048576\n"},
			wantErr: true,
		},
		{
			name: "malformed anon",
			files: map[string]string{
				"memory.stat": "anon garbage\n",
			},
			wantErr: true,
		},
		{
			// An oom_kill line that is not a number reads as zero rather than inventing a count.
			name: "malformed oom_kill keeps the counter at zero",
			files: map[string]string{
				"memory.stat":   "anon 1048576\n",
				"memory.events": "oom_kill nope\n",
			},
			want: CgroupMemory{AnonBytes: 1 * mb},
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

// TestCgroupMemoryHeldBytes checks the definition of held memory: what the sandbox itself holds,
// which is not the same as the file total the cgroup reports.
func TestCgroupMemoryHeldBytes(t *testing.T) {
	const mb = int64(1024 * 1024)
	mem := CgroupMemory{AnonBytes: 17 * mb, ShmemBytes: 200 * mb}
	if got := mem.HeldBytes(); got != 217*mb {
		t.Errorf("HeldBytes() = %d MB, want 217 MB", got/mb)
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
	// A real cgroup has a memory.high file whether or not a limit was set, so the fixture has one.
	if err := os.WriteFile(filepath.Join(dir, "memory.high"), []byte("536870912"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon 1048576\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := clearCgroupMemoryLimit(dir); err != nil {
		t.Fatalf("clearCgroupMemoryLimit() = %v", err)
	}

	for _, f := range []string{"memory.max", "memory.high", "memory.swap.max"} {
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

// The limit and memory.swap.max are what enforce the cap; memory.high is cleared rather than set.
func TestApplyMemoryLimitAt(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"memory.max", "memory.high", "memory.swap.max"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("536870912"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := applyMemoryLimitAt(dir, "kanban-task-x", 512); err != nil {
		t.Fatalf("applyMemoryLimitAt() = %v", err)
	}

	const mb = int64(1) << 20
	read := func(f string) string {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		return string(data)
	}
	if got := read("memory.max"); got != strconv.FormatInt(512*mb, 10) {
		t.Errorf("memory.max = %s, want %d", got, 512*mb)
	}
	if got := read("memory.swap.max"); got != "0" {
		t.Errorf("memory.swap.max = %q, want 0", got)
	}
	if got := read("memory.high"); got != "max" {
		t.Errorf("memory.high = %q, want max — a throttle line stalls an unreclaimable guest", got)
	}

	// A limit of zero is the caller's "no limit" signal, not a write of zero bytes.
	if err := applyMemoryLimitAt(dir, "kanban-task-x", 0); err == nil {
		t.Error("expected an error for a non-positive limit")
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

// TestGVisorAvailable_FalseWhenVersionCheckFails covers the second gate: runsc present but unusable.
// Both sandbox managers read this one probe, so they cannot disagree about a broken install.
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

	rt := NewRuntimeWithBoot(Boot{})
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

// TestBootDeclinesTheHostSweepByDefault pins the shape of the claim: the boot sweep deletes every
// stale kanban-* container family, which is a host-wide delete, so a Runtime built without a Boot
// declines it. That is what keeps a test binary from wiping a live server's sandboxes by forgetting to
// set a variable, and both sandbox managers are handed the same value from startup.Config, so the task
// family and the extension family stay under one decision.
func TestBootDeclinesTheHostSweepByDefault(t *testing.T) {
	if NewRuntimeWithBoot(Boot{}).Boot().SweepStaleContainersAtBoot {
		t.Error("the zero Boot claims the host's container namespace")
	}
	if NewRuntimeWithBoot(Boot{}).Boot().AgentPath != "" {
		t.Error("the zero Boot overrides where the bridge binary is found")
	}

	claim := Boot{SweepStaleContainersAtBoot: true, AgentPath: "/opt/bridge/sandbox-bridge"}
	rt := NewRuntimeWithBoot(claim)
	if !rt.Boot().SweepStaleContainersAtBoot {
		t.Error("NewRuntimeWithBoot lost the claim it was built with")
	}
	if rt.Boot().AgentPath != claim.AgentPath {
		t.Errorf("Boot().AgentPath = %q, want %q", rt.Boot().AgentPath, claim.AgentPath)
	}
}

// TestGetAgentBinaryPathPrefersTheWiredPath: the override is used when the file is there and ignored
// when it is not, so a stale MIKE_AGENT_PATH-style override cannot point a task at the wrong binary.
func TestGetAgentBinaryPathPrefersTheWiredPath(t *testing.T) {
	bridge := filepath.Join(t.TempDir(), "sandbox-bridge")
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := NewRuntimeWithBoot(Boot{AgentPath: bridge}).GetAgentBinaryPath()
	if err != nil {
		t.Fatalf("GetAgentBinaryPath with the wired path: %v", err)
	}
	if got != bridge {
		t.Errorf("GetAgentBinaryPath = %q, want the wired path %q", got, bridge)
	}

	missing := filepath.Join(t.TempDir(), "not-here")
	if got, err := NewRuntimeWithBoot(Boot{AgentPath: missing}).GetAgentBinaryPath(); err == nil && got == missing {
		t.Error("a wired path that does not exist was used instead of the search fallback")
	}
}
