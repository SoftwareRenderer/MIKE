package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

func buildTestProject(repo string) *types.Project {
	return &types.Project{
		ID:                    "buildproj",
		Name:                  "buildproj",
		Path:                  repo,
		SandboxEnabled:        true,
		SandboxDockerfilePath: filepath.Join(repo, "Dockerfile"),
	}
}

// useScratchRoot points MIKE's build scratch at a test directory. The scratch root is injected and starts
// unset, with builds refused until something sets it, so a test that plans a build has to say where it writes.
func useScratchRoot(t *testing.T) {
	t.Helper()
	previous := buildScratchRoot
	SetScratchRoot(t.TempDir())
	t.Cleanup(func() { SetScratchRoot(previous) })
}

func testPlan(t *testing.T, project *types.Project) *buildPlan {
	t.Helper()
	useScratchRoot(t)
	scratch, err := newBuildScratch(project.ID, project.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scratch.Remove)
	return &buildPlan{
		project:       project,
		scratch:       scratch,
		contextRoot:   ".",
		dockerfileRel: "Dockerfile",
		containerName: sandboxruntime.BuildContainerName(project.ID, scratch.ID),
	}
}

// A `FROM` ref reaches a podman argument, so its shape is checked and what it names is not.
func TestBuildRefusesAFROMRefThatCannotBeAnArgument(t *testing.T) {
	for _, bad := range []string{"--privileged", "-x", "a b", "", "a;rm -rf /", "$(id)", "alpine:3.20 --privileged"} {
		if err := validateBuildBase(bad); err == nil {
			t.Errorf("accepted FROM %q", bad)
		}
	}
	for _, good := range []string{
		"alpine", "alpine:latest", "alpine:3.20", "docker.io/library/ubuntu:latest",
		"docker.io/library/alpine:3.20", "quay.io/buildah/stable:v1.40.0",
		"ghcr.io/o/r@sha256:" + strings.Repeat("a", 64), "scratch",
	} {
		if err := validateBuildBase(good); err != nil {
			t.Errorf("refused FROM %q: %v", good, err)
		}
	}
}

// The key covers the commit, the context tree, the images and the Dockerfile's own bytes: `ENV FOO=1` changes the
// image and appears nowhere else in it.
func TestBuildCacheKeyChangesWithTheContextRoot(t *testing.T) {
	const dockerfile = "FROM alpine:3.20\nRUN make\n"
	bases := []string{"alpine:3.20"}
	base := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("2", 40), dockerfile, bases)
	otherCommit := buildCacheKey(strings.Repeat("9", 40), strings.Repeat("2", 40), dockerfile, bases)
	otherTree := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("8", 40), dockerfile, bases)
	otherFrom := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("2", 40), dockerfile, []string{"alpine:3.21"})
	otherRun := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("2", 40), dockerfile+"RUN test\n", bases)
	unread := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("2", 40), dockerfile+"ENV FOO=1\n", bases)
	unchanged := buildCacheKey(strings.Repeat("1", 40), strings.Repeat("2", 40), dockerfile, bases)

	if base != unchanged {
		t.Error("the same build produced two keys")
	}
	for name, other := range map[string]string{
		"a new commit": otherCommit, "a changed context subtree": otherTree,
		"a different base image": otherFrom, "one more RUN step": otherRun,
		"an instruction MIKE does not model": unread,
	} {
		if other == base {
			t.Errorf("%s did not change the cache key", name)
		}
	}
}

func TestBuildDoesNotCreateAMissingHostMount(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")
	project := buildTestProject(t.TempDir())
	project.SandboxMounts = []types.SandboxMount{{HostPath: missing, ContainerPath: "/mnt/x"}}
	err := checkBuildMounts(project)
	if err == nil {
		t.Fatal("a build accepted a mount whose host directory does not exist")
	}
	if !strings.Contains(err.Error(), "will not create host directories") {
		t.Errorf("the refusal does not say the build creates nothing: %v", err)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("the refusal check created the host directory")
	}
	project.SandboxMounts = nil
	if err := checkBuildMounts(project); err != nil {
		t.Errorf("a project with no mounts was refused: %v", err)
	}
}

// One table, one row per Sandbox Configuration field, so a new column that fails to reach the build fails
// here rather than in a project that expected it. Capabilities are the one column that must not reach it.
func TestBuildContainerUsesProjectOptsButNeverTheProjectsCapabilities(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), available: true}
	project := buildTestProject(t.TempDir())
	project.SandboxMemoryLimit = 1536
	project.SandboxNetworkIsolation = true
	// A name outside the build's fixed set, so its presence in the argv would say the project's list arrived.
	project.SandboxCapabilities = []string{"SYS_ADMIN"}
	project.SandboxEnvs = []types.SandboxEnv{{Key: "BUILD_TRACE", Value: "1"}}
	project.ForwardPorts = []types.PortForward{{ContainerPort: 80}}
	hostDir := t.TempDir()
	project.SandboxMounts = []types.SandboxMount{{HostPath: hostDir, ContainerPath: "/mnt/data", Mode: "rw"}}

	plan := testPlan(t, project)
	plan.baseArchives = []string{writeBuildBaseArchive(t, plan, 0)}
	plan.baseNames = [][]string{{"docker.io/library/alpine:3.20", "alpine:3.20"}}
	args, err := sm.buildContainerArgs(plan)
	if err != nil {
		t.Fatalf("the build container's arguments were refused: %v", err)
	}
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--runtime runsc", "--cap-drop ALL",
		"--memory 1536M", "BUILD_TRACE=1",
		"--security-opt no-new-privileges", "--pull never",
		plan.ContextDir() + ":" + buildContextMount + ":rprivate,ro",
		plan.scratch.Output + ":" + buildOutputMount + ":rprivate,rw",
		hostDir + ":/mnt/data:rprivate,ro",
	} {
		if want == "" {
			continue
		}
		if !strings.Contains(joined, want) {
			t.Errorf("the build container's argv is missing %q\n%s", want, joined)
		}
	}

	// The grant is the build's own, in full.
	for _, cap := range sandboxruntime.BuilderGrant {
		if !strings.Contains(joined, "--cap-add "+cap+" ") && !strings.HasSuffix(joined, "--cap-add "+cap) {
			t.Errorf("the build container's argv is missing its grant of %s\n%s", cap, joined)
		}
	}
	// A capability of the task container is not a capability of the build: one name arriving here is the
	// union that moved buildah onto the path a guest cannot take (docs/adr/0002).
	if strings.Contains(joined, "--cap-add SYS_ADMIN") {
		t.Errorf("a configured capability reached the build container: %s", joined)
	}
}

// The build container writes one host path and nothing else: the staged context, the project's mounts and
// the base archive are read-only, whatever mode a mount row spells.
func TestBuildMountsEverythingReadOnlyEvenWhenTheRowSaysRw(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), available: true}
	project := buildTestProject(t.TempDir())
	hostDir := t.TempDir()
	project.SandboxMounts = []types.SandboxMount{
		{HostPath: hostDir, ContainerPath: "/mnt/rw", Mode: "rw"},
		{HostPath: hostDir, ContainerPath: "/mnt/unset"},
	}
	plan := testPlan(t, project)
	args, err := sm.buildContainerArgs(plan)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, ro := range []string{hostDir + ":/mnt/rw:rprivate,ro", hostDir + ":/mnt/unset:rprivate,ro"} {
		if !strings.Contains(joined, ro) {
			t.Errorf("mount is not read-only: %s\n%s", ro, joined)
		}
	}
	if strings.Contains(joined, ":rprivate,rw,"+hostDir) || strings.Contains(joined, hostDir+":/mnt/rw:rprivate,rw") {
		t.Errorf("a project mount reached the build writable:\n%s", joined)
	}
}

// A build publishes nothing. Ports are how a container reaches the host's network, and a build has no
// reason to hold one.
func TestBuildContainerPublishesNoPorts(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), available: true}
	project := buildTestProject(t.TempDir())
	project.ForwardPorts = []types.PortForward{{ContainerPort: 80}, {ContainerPort: 90}}
	plan := testPlan(t, project)
	args, err := sm.buildContainerArgs(plan)
	if err != nil {
		t.Fatal(err)
	}
	// Checked per argument, not over the joined line: the guest command is one argument full of flags of
	// its own, and a substring test there finds shapes that are not podman arguments.
	for _, arg := range args[:len(args)-3] {
		if arg == "-p" || arg == "--publish" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--publish=") {
			t.Errorf("a build container published a port: %q", arg)
		}
	}
}

// The guest command is what actually executes, so its shape is worth pinning: no pull, the archive as the
// only network-shaped input, and the push as the only exit.
func TestBuildContainerScriptPinsTheGuestContract(t *testing.T) {
	project := buildTestProject(t.TempDir())
	plan := testPlan(t, project)
	plan.fromRef = "alpine:3.20"
	plan.baseArchives = []string{writeBuildBaseArchive(t, plan, 0), writeBuildBaseArchive(t, plan, 1)}
	plan.baseNames = [][]string{
		{"docker.io/library/alpine:3.20"},
		{"docker.io/library/golang:1.23", "golang:1.23"},
	}
	plan.cacheKey = "deadbeef"
	plan.commit = strings.Repeat("b", 40)
	plan.baseImageIDs = []string{"sha256:" + strings.Repeat("c", 64)}

	script := buildContainerScript(plan)
	for _, want := range []string{
		"--isolation chroot", "--pull=never",
		"docker-archive:" + buildBaseMount + "/base-0.tar",
		// A multi-stage file names more than one image, and each has to arrive, be identified by its own
		// pull (not by "whatever is first in the store") and answer to the spelling the file used.
		"docker-archive:" + buildBaseMount + "/base-1.tar",
		"tag \"$id\" 'docker.io/library/golang:1.23'", "tag \"$id\" 'golang:1.23'",
		"bud ", "-t " + buildImageTag,
		"push " + buildImageTag + " docker-archive:" + buildOutputMount + "/image.tar",
		"--label " + sandboxruntime.ShellQuote(buildCacheKeyLabel+"=deadbeef"),
		// The base image's id is the answer to "which bytes was this built on", which a floating tag leaves
		// the ref unable to give.
		"--label " + sandboxruntime.ShellQuote(buildBaseLabel+"=sha256:"+strings.Repeat("c", 64)),
		"--storage-driver vfs", "--root " + buildStoreRoot + "/s",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the guest command is missing %q:\n%s", want, script)
		}
	}
	// The store lives on the container's rootfs. Under the guest's tmpfs a layer's xattr read fails, which
	// is how the /tmp variant was ruled out (docs/adr/0002).
	for _, root := range []string{"--root " + buildStoreRoot + "/s", "--runroot " + buildStoreRoot + "/r"} {
		if !strings.Contains(script, root) {
			t.Errorf("the build store is not on the container's rootfs (%s):\n%s", root, script)
		}
	}
	if strings.Contains(script, "network") {
		t.Errorf("the guest command reaches for a network:\n%s", script)
	}

	// `FROM scratch` needs no base image handed in.
	scratchPlan := testPlan(t, project)
	scratchPlan.fromRef = "scratch"
	if strings.Contains(buildContainerScript(scratchPlan), "pull docker-archive") {
		t.Error("a scratch build reaches for a base archive it does not have")
	}
	if strings.Contains(buildContainerScript(scratchPlan), buildBaseLabel) {
		t.Error("a scratch build claims a base image it does not have")
	}
}

func TestLoadedImageNameReadsPodmansOwnReport(t *testing.T) {
	got, err := loadedImageName("Getting image source signatures\nLoaded image: localhost/mike-build:latest\n")
	if err != nil || got != "localhost/mike-build:latest" {
		t.Fatalf("parsed %q, err %v", got, err)
	}
	if _, err := loadedImageName("Loaded image ID: 3b9a4c2d1e"); err == nil {
		t.Error("an untagged load was accepted; the image could not be retagged")
	}
	if _, err := loadedImageName("podman: command not found"); err == nil {
		t.Error("a load that reported nothing was accepted")
	}
}

func TestBuildContainerNameIsSweptAndTraceable(t *testing.T) {
	name := sandboxruntime.BuildContainerName("proj-MIKE", "proj-MIKE-1730000000-1")
	if !strings.HasPrefix(name, sandboxruntime.StaleBuildFilter) {
		t.Errorf("%s is outside the sweep filter %s", name, sandboxruntime.StaleBuildFilter)
	}
	if !strings.Contains(name, "proj-MIKE") {
		t.Errorf("%s does not say which project left it", name)
	}
	if strings.Contains(sandboxruntime.StaleBuildFilter, "kanban-sandbox") {
		t.Error("the build sweep filter would delete task sandboxes")
	}
}

func TestScratchRootIsInjectedAndItsZeroValueDeclines(t *testing.T) {
	previous := buildScratchRoot
	t.Cleanup(func() { SetScratchRoot(previous) })
	SetScratchRoot("")
	if _, err := newBuildScratch("proj", "proj"); err == nil {
		t.Fatal("a build was planned with no scratch root")
	}
	root := t.TempDir()
	SetScratchRoot(root)
	scratch, err := newBuildScratch("proj", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(scratch.Dir, root) {
		t.Errorf("scratch escaped the configured root: %s", scratch.Dir)
	}
	if info, statErr := os.Stat(scratch.Dir); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("scratch directory mode = %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(scratch.Archive()); err == nil {
		t.Error("the archive exists before the build wrote it")
	}
	scratch.Remove()
	if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
		t.Error("Remove left the scratch directory behind")
	}
}

// The scratch root must never be the data directory: it holds the database, its backups and the TLS key,
// and a build's disk usage is chosen by a repository.
func TestBuildScratchNeverLivesUnderTheDataDir(t *testing.T) {
	previous := buildScratchRoot
	t.Cleanup(func() { SetScratchRoot(previous) })

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "kanban.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetScratchRoot(dataDir)
	scratch, err := newBuildScratch("proj", "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scratch.Remove)

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "kanban.db" || e.Name() == "tls" {
			continue
		}
		if e.Name() != buildScratchRootName {
			t.Errorf("the scratch root wrote %s beside the database", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "kanban.db")); err != nil {
		t.Error("the database was removed by the scratch path")
	}
}

func TestScratchSweepRemovesOrphansAndKeepsNothingElse(t *testing.T) {
	previous := buildScratchRoot
	t.Cleanup(func() { SetScratchRoot(previous) })

	root := t.TempDir()
	SetScratchRoot(root)
	orphan, err := newBuildScratch("dead-proj", "dead-proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan.Archive(), []byte("half an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Something else that happens to live in the workspace is not MIKE's build scratch.
	elsewhere := filepath.Join(root, "checkouts")
	if err := os.MkdirAll(filepath.Join(elsewhere, "keep-me"), 0o755); err != nil {
		t.Fatal(err)
	}

	SweepScratch()

	if _, err := os.Stat(orphan.Dir); !os.IsNotExist(err) {
		t.Error("an orphaned build scratch survived the boot sweep")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "keep-me")); err != nil {
		t.Error("the sweep removed a directory that is not build scratch")
	}
	SweepScratch() // an absent root sweeps nothing and reports nothing
}

func TestScratchWatchStopsTheBuildPastTheCap(t *testing.T) {
	previous := buildScratchRoot
	t.Cleanup(func() { SetScratchRoot(previous) })
	SetScratchRoot(t.TempDir())
	scratch, err := newBuildScratch("proj", "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scratch.Remove)

	fired := make(chan struct{}, 1)
	scratch.Watch(1024, func() { fired <- struct{}{} })
	if err := os.WriteFile(filepath.Join(scratch.Output, "image.tar"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(15 * time.Second):
		scratch.StopWatch()
		t.Fatal("the output cap was never reached, or nothing was watching")
	}
	scratch.StopWatch()
	if scratch.peak.Load() < 4096 {
		t.Errorf("the peak was not recorded: %d", scratch.peak.Load())
	}
}

func TestBuildCancelReachesAnInFlightBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forget := registerBuild("build-1", cancel)
	if ActiveBuildCount() != 1 {
		t.Fatalf("the build is not tracked")
	}
	CancelBuilds()
	select {
	case <-ctx.Done():
	default:
		t.Error("CancelBuilds did not cancel the build's context")
	}
	forget()
	if ActiveBuildCount() != 0 {
		t.Error("a finished build is still tracked")
	}
}

// The build's grant is fixed, so a permission refusal cannot be answered by pointing the person at a list:
// it names what the grant is and what is left to look at.
func TestBuildFailureNamesTheFixedGrant(t *testing.T) {
	denied := []string{
		"STEP 2/6: RUN tar -cf /d.tar /d",
		`time="2026-10-30T22:42:15Z" level=error msg="While applying layer: lchown /etc/shadow: operation not permitted"`,
	}
	hint := buildFailureHint(denied)
	for _, want := range []string{
		fmt.Sprintf("%d capability grants", len(sandboxruntime.BuilderGrant)),
		"reads none of this project's Capabilities",
		"changes nothing here",
		"RUN step",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint does not name %q: %q", want, hint)
		}
	}
	// A failure that is not about permissions is not the grant's.
	other := []string{"COPY failed: no source files were specified", "error: invalid reference format"}
	if got := buildFailureHint(other); got != "" {
		t.Errorf("an unrelated failure was blamed on the grant: %q", got)
	}
	// The user-namespace refusal is the same class: it is what a nested build that cannot make one looks like.
	if got := buildFailureHint([]string{`writing "0 0 4294967295" to /proc/15/gid_map: operation not permitted`}); got == "" {
		t.Error("the uid_map/gid_map refusal was left without an answer")
	}
}

// writeBuildBaseArchive puts a file where the build expects an image archive, so a test of the arguments and
// the guest script does not need an image. The base directory exists from the scratch directory's creation;
// this only writes into it.
func writeBuildBaseArchive(t *testing.T, plan *buildPlan, i int) string {
	t.Helper()
	archive := plan.scratch.BaseArchiveAt(i)
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	return archive
}

// `sandbox_network_isolation` governs the agent's containers, not the build, so it must not appear in the build's
// arguments either way. Re-coupling them would leave an isolated project's builds unable to install anything.
func TestBuildHasANetworkEvenForAnIsolatedProject(t *testing.T) {
	project := buildTestProject(t.TempDir())
	isolated := *project
	isolated.SandboxNetworkIsolation = true

	sm := NewSandboxManager(bootWithBridge(t))
	for name, p := range map[string]*types.Project{"networked": project, "isolated": &isolated} {
		args, err := sm.buildContainerArgs(testPlan(t, p))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, a := range args {
			if a == "none" {
				t.Errorf("the %s project's build was given no network, so its RUN steps cannot install anything", name)
			}
			if a == "-p" || a == "--publish" || strings.HasPrefix(a, "-p=") || strings.HasPrefix(a, "--publish=") {
				t.Errorf("the %s project's build publishes a port (%s)", name, a)
			}
		}
	}
}
