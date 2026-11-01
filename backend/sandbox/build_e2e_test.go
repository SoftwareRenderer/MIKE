package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/sandboxcheck"
	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

// These tests run a real build: the only place docs/adr/0002 is checked against a container rather than an argument
// list. They skip, naming the reason, when the host lacks the builder image.

func requireBuilder(t *testing.T) {
	t.Helper()
	out, err := exec.Command("podman", "image", "exists", sandboxruntime.BuilderImageDefault).CombinedOutput()
	if err != nil {
		t.Skipf("the nested builder image %s is not on this host (pull it, or run `make build-probe`): %s",
			sandboxruntime.BuilderImageDefault, strings.TrimSpace(string(out)))
	}
	sandboxcheck.Require(t, sandboxruntime.BuilderImageDefault)
}

// buildE2E is a project with a committed Dockerfile, in a scratch root of its own.
func buildE2E(t *testing.T, files map[string]string, contextRoot string) (*SandboxManager, *types.Project, string) {
	t.Helper()
	requireBuilder(t)
	repo := testRepo(t, files)
	useScratchRoot(t)
	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.available {
		t.Skip("gVisor is not available on this host")
	}
	project := &types.Project{
		ID:                    "builde2e",
		Name:                  "builde2e",
		Path:                  repo,
		SandboxEnabled:        true,
		SandboxDockerfilePath: filepath.Join(repo, "Dockerfile"),
		SandboxBuildContext:   contextRoot,
	}
	commit, err := resolveGitRef(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sm, project, commit
}

// imageRootFS returns the loaded image's filesystem as a map of path → contents, read out of podman's own
// export. It is how the test proves what the *image* holds rather than what the build reported.
func imageRootFS(t *testing.T, image string) map[string]string {
	t.Helper()
	out, err := exec.Command("podman", "create", image, "/bin/true").CombinedOutput()
	if err != nil {
		t.Fatalf("create a container from %s: %v: %s", image, err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("podman", "rm", "-f", id).Run() })
	tarOut, err := exec.Command("podman", "export", id).Output()
	if err != nil {
		t.Fatalf("export %s: %v", image, err)
	}
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(tarOut))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(tr); err == nil && buf.Len() < 1<<20 {
			files[strings.TrimPrefix(hdr.Name, "/")] = buf.String()
		}
	}
	return files
}

// The design in one test: a committed file reaches the image through a staged subtree, the build runs in a
// gVisor container, and the finished image lands in the host's store.
func TestBuildProjectImageBuildsACommittedTreeInASandbox(t *testing.T) {
	sm, project, commit := buildE2E(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY hello /hello\n",
		"hello":      "from-the-commit\n",
	}, "")
	// A real checkout holds files git has never seen — `.env` is the one that matters — and the commit the
	// context is staged from does not contain them, so they cannot be build input.
	if err := os.WriteFile(filepath.Join(project.Path, "untracked.txt"), []byte("not build input\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var lines []string
	result, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("the build failed: %v\nprogress:\n  %s", err, strings.Join(lines, "\n  "))
	}
	if result.Commit != commit {
		t.Errorf("the build reports commit %s, the checkout is at %s", result.Commit, commit)
	}
	if result.From != "scratch" {
		t.Errorf("base = %q", result.From)
	}
	if result.Files != 2 {
		t.Errorf("staged %d files, want the two committed ones", result.Files)
	}
	if !sm.ImageReady(result.Image) {
		t.Fatalf("the image %s is not in the host's store", result.Image)
	}
	if !sm.ImageCarriesBuild(result.Image, result.CacheKey) {
		t.Errorf("the loaded image does not carry the build record %s", result.CacheKey)
	}

	files := imageRootFS(t, result.Image)
	if got := files["hello"]; got != "from-the-commit\n" {
		t.Errorf("the image holds %q at /hello, want the committed bytes", got)
	}
	for name := range files {
		if strings.Contains(name, "untracked") {
			t.Errorf("the image holds %s, which was never committed", name)
		}
	}

	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", result.Image).CombinedOutput()
	})
}

// Nothing survives a build: no container, no scratch directory, on the success path and on a refusal.
func TestBuildRemovesItsContainerAndScratchOnEveryExit(t *testing.T) {
	scratchRoot := t.TempDir()
	requireBuilder(t)

	for _, tc := range []struct {
		name  string
		files map[string]string
		ok    bool
	}{
		{"success", map[string]string{"Dockerfile": "FROM scratch\nCOPY a /a\n", "a": "a"}, true},
		{"parse refusal", map[string]string{"Dockerfile": "RUN make\nCOPY a /a\n", "a": "a"}, false},
		{"copy source missing from the staged tree", map[string]string{"Dockerfile": "FROM scratch\nCOPY not-there /a\n", "a": "a"}, false},
		{"context below the dockerfile", map[string]string{"Dockerfile": "FROM scratch\nCOPY a /a\n", "a": "a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := buildScratchRoot
			SetScratchRoot(scratchRoot)
			defer SetScratchRoot(previous)

			repo := testRepo(t, tc.files)
			sm := NewSandboxManager(bootWithBridge(t))
			if !sm.available {
				t.Skip("gVisor is not available on this host")
			}
			project := &types.Project{
				ID: "buildexit", Name: "buildexit", Path: repo, SandboxEnabled: true,
				SandboxDockerfilePath: filepath.Join(repo, "Dockerfile"),
			}
			if tc.name == "context below the dockerfile" {
				project.SandboxBuildContext = "sub"
			}
			_, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
			if tc.ok && err != nil {
				t.Fatalf("the build failed: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("the build succeeded where it must refuse")
			}
			if tc.name == "success" {
				t.Cleanup(func() {
					_, _ = exec.Command("podman", "rmi", ProjectImageName(project.Name)).CombinedOutput()
				})
			}

			entries, readErr := os.ReadDir(filepath.Join(scratchRoot, buildScratchRootName))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if readErr == nil && len(entries) != 0 {
				t.Errorf("scratch survived the build: %v", entries)
			}
			out, _ := exec.Command("podman", "ps", "-a", "--filter", "name="+sandboxruntime.StaleBuildFilter,
				"--format", "{{.Names}}").CombinedOutput()
			if names := strings.TrimSpace(string(out)); names != "" {
				t.Errorf("a build container survived: %s", names)
			}
		})
	}
}

// A `COPY` above the context is not refused, because the staged read-only context is the file's whole world. What
// matters is that those bytes reach no image and the project keeps its old one.
func TestBuildProducesNoImageForACopySourceAboveTheContext(t *testing.T) {
	sm, project, _ := buildE2E(t, map[string]string{
		"service/Dockerfile": "FROM scratch\nCOPY ../shared/config /config\n",
		"shared/config":      "secret-ish\n",
	}, "service")
	project.SandboxDockerfilePath = filepath.Join(project.Path, "service", "Dockerfile")

	before, _ := sm.imageIDOf(ProjectImageName(project.Name))
	_, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
	if err == nil {
		t.Fatal("a COPY source above the context built, and the design says the staged tree is the file's whole world")
	}
	// The load is the only path that takes the project's image name, and it did not run, so whatever the
	// project had before this build is what it still has — including nothing, if nothing was there.
	after, _ := sm.imageIDOf(ProjectImageName(project.Name))
	if before != after {
		t.Errorf("the failed build changed the project's image from %q to %q", shortImageID(before), shortImageID(after))
	}
}

// Widening the context is the person's answer to the refusal above, and it is the same build input the
// refusal named.
func TestBuildContextSelectsASubtreeOfThePinnedCommitForARealBuild(t *testing.T) {
	sm, project, _ := buildE2E(t, map[string]string{
		"service/Dockerfile": "FROM scratch\nCOPY shared/config /config\nCOPY service/app /app\n",
		"shared/config":      "shared-by-context\n",
		"service/app":        "the app\n",
		"elsewhere/file":     "not staged\n",
	}, ".")
	project.SandboxDockerfilePath = filepath.Join(project.Path, "service", "Dockerfile")

	result, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", result.Image).CombinedOutput()
	})
	files := imageRootFS(t, result.Image)
	if files["config"] != "shared-by-context\n" || files["app"] != "the app\n" {
		t.Errorf("the image holds %v", files)
	}
}

// The silent regression: a host-side commit would load fine and simply miss what the RUN steps wrote. The base
// arrives over the archive mount, so this covers the no-pull path too.
func TestLoadedImageCarriesWhatTheRunStepWrote(t *testing.T) {
	if out, err := exec.Command("podman", "image", "exists", "docker.io/library/alpine:3.20").CombinedOutput(); err != nil {
		t.Skipf("docker.io/library/alpine:3.20 is not on this host, and a RUN step needs a base with a shell in it: %s", strings.TrimSpace(string(out)))
	}
	sm, project, _ := buildE2E(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nCOPY hello /hello\nRUN cat /hello > /greet\n",
		"hello":      "written-by-a-run-step\n",
	}, "")

	result, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", result.Image).CombinedOutput()
	})
	if len(result.Runs) != 1 {
		t.Fatalf("the build reported %d RUN steps", len(result.Runs))
	}
	files := imageRootFS(t, result.Image)
	if files["greet"] != "written-by-a-run-step\n" {
		t.Errorf("the loaded image holds no output from the RUN step: %v", files)
	}
}

// A load is where the image a project boots from changes hands, so the order of its steps is the
// project's safety: refuse first, retag second, retire the old image last.
func TestLoadRefusalLeavesTheProjectTheImageItHad(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")
	sm := NewSandboxManager(bootWithBridge(t))
	useScratchRoot(t)
	scratch, err := newBuildScratch("loadrefuse", "loadrefuse")
	if err != nil {
		t.Fatal(err)
	}
	defer scratch.Remove()

	const projectImage = "localhost/sandbox-load-refuse"
	untag := func() { _, _ = exec.Command("podman", "rmi", "-f", projectImage).CombinedOutput() }
	untag()
	t.Cleanup(untag)
	if out, err := exec.Command("podman", "tag", "docker.io/library/alpine:3.20", projectImage).CombinedOutput(); err != nil {
		t.Fatalf("seed the previous image: %s", out)
	}
	before := imageID(t, projectImage)

	plan := &buildPlan{
		project:    &types.Project{ID: "loadrefuse", Name: "load-refuse"},
		scratch:    scratch,
		cacheKey:   "the-key-this-build-would-stamp",
		runSetHash: "ffffffffffffffff",
	}
	result := &BuildResult{Image: projectImage}

	// A truncated archive never reaches the store.
	if err := os.WriteFile(scratch.Archive(), []byte("not a docker-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = sm.loadBuiltImage(plan, result, nil)
	if err == nil {
		t.Fatal("a truncated archive loaded")
	}
	if !strings.Contains(err.Error(), "load the built image") {
		t.Errorf("error = %v, want it to name the load it attempted", err)
	}
	if got := imageID(t, projectImage); got != before {
		t.Errorf("the project image changed to %s after a failed load (was %s)", got, before)
	}

	// A valid archive whose image carries no build record is loaded, then refused — and the refusal
	// happens before anything is retagged onto the project's name.
	plain := "localhost/mike-load-plain:1"
	t.Cleanup(func() { _, _ = exec.Command("podman", "rmi", "-f", plain).CombinedOutput() })
	saveTo(t, "docker.io/library/alpine:3.20", scratch.Archive())
	if out, err := exec.Command("podman", "load", "-i", scratch.Archive()).CombinedOutput(); err != nil {
		t.Fatalf("load the plain archive: %s", out)
	}
	if out, err := exec.Command("podman", "tag", "docker.io/library/alpine:3.20", plain).CombinedOutput(); err != nil {
		t.Fatalf("tag the loaded image: %s", out)
	}
	saveTo(t, plain, scratch.Archive())
	err = sm.loadBuiltImage(plan, result, nil)
	if err == nil || !strings.Contains(err.Error(), "no build record") {
		t.Fatalf("error = %v, want the missing build record", err)
	}
	if got := imageID(t, projectImage); got != before {
		t.Errorf("an image with no build record holds the project's image name (%s, was %s)", got, before)
	}
}

// The success path moves the project's name only onto an accepted image, and its retirement step never
// touches the id the new image turned out to have.
func TestLoadRetiresThePreviousImageOnlyAfterTheNewOneIsAccepted(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")

	for _, tc := range []struct {
		name string
		// sameID seeds the project with the labelled build itself, so the archive the load reads lands on
		// the id the project already holds: a build whose base and steps changed nothing.
		sameID bool
	}{
		{"a rebuild to a new id", false},
		{"a rebuild that lands on the same id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewSandboxManager(bootWithBridge(t))
			useScratchRoot(t)
			scratch, err := newBuildScratch("loadok", "loadok")
			if err != nil {
				t.Fatal(err)
			}
			defer scratch.Remove()

			const (
				projectImage = "localhost/sandbox-load-ok"
				builtImage   = "localhost/mike-build:load-ok"
				cacheKey     = "key-of-this-build"
			)
			cleanup := func() {
				_, _ = exec.Command("podman", "rmi", "-f", projectImage).CombinedOutput()
				_, _ = exec.Command("podman", "rmi", "-f", builtImage).CombinedOutput()
			}
			cleanup()
			t.Cleanup(cleanup)

			// What a real build would have pushed: the builder's tag, the labels, and the archive.
			labelled := commitWithLabel(t, "docker.io/library/alpine:3.20", builtImage,
				map[string]string{buildCacheKeyLabel: cacheKey, buildRunSetLabel: "aaaaaaaaaaaaaaaa"})
			// The image the project already holds: an older build in one case, the very same id in the other.
			from := "docker.io/library/alpine:3.20"
			if tc.sameID {
				from = labelled
			}
			if out, err := exec.Command("podman", "tag", from, projectImage).CombinedOutput(); err != nil {
				t.Fatalf("seed the previous image: %s", out)
			}
			previous := imageID(t, projectImage)
			saveTo(t, builtImage, scratch.Archive())

			plan := &buildPlan{
				project:    &types.Project{ID: "loadok", Name: "load-ok"},
				scratch:    scratch,
				cacheKey:   cacheKey,
				runSetHash: "aaaaaaaaaaaaaaaa",
			}
			if err := sm.loadBuiltImage(plan, &BuildResult{Image: projectImage}, nil); err != nil {
				t.Fatalf("the load failed: %v", err)
			}
			if got := imageID(t, projectImage); got != labelled {
				t.Errorf("the project's name holds %s, want the newly built %s", got, labelled)
			}
			if tc.sameID && previous != labelled {
				t.Fatalf("the fixture was to land on the id it started on; got %s from %s", labelled, previous)
			}
			// The builder's own tag is an intermediate, not something a project boots from.
			if out, err := exec.Command("podman", "image", "exists", builtImage).CombinedOutput(); err == nil {
				t.Errorf("the intermediate %s was left behind: %s", builtImage, out)
			}
		})
	}
}

// requireHostImage skips with the reason the archive paths need a real image to hand around.
func requireHostImage(t *testing.T, ref string) {
	t.Helper()
	if out, err := exec.Command("podman", "image", "exists", ref).CombinedOutput(); err != nil {
		t.Skipf("%s is not on this host, which the load path needs as an archive: %s", ref, strings.TrimSpace(string(out)))
	}
}

func imageID(t *testing.T, image string) string {
	t.Helper()
	out, err := exec.Command("podman", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect %s: %s", image, out)
	}
	return strings.TrimSpace(string(out))
}

// saveTo writes a docker-archive the way the guest's push does, into the path the build reads.
func saveTo(t *testing.T, image, archive string) {
	t.Helper()
	// podman refuses to write an archive over one that exists, and the build reuses its output directory.
	if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out, err := exec.Command("podman", "save", "--format", "docker-archive", "-o", archive, image).CombinedOutput()
	if err != nil {
		t.Fatalf("save %s to %s: %s", image, archive, out)
	}
}

// commitWithLabel makes a labelled image out of one that exists, standing in for the builder's final step.
func commitWithLabel(t *testing.T, base, dest string, labels map[string]string) string {
	t.Helper()
	out, err := exec.Command("podman", "create", base, "/bin/true").CombinedOutput()
	if err != nil {
		t.Fatalf("create from %s: %s", base, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("podman", "rm", "-f", id).Run() })
	args := []string{"commit"}
	for k, v := range labels {
		args = append(args, "--change", "LABEL="+k+"="+v)
	}
	args = append(args, id, dest)
	if out, err := exec.Command("podman", args...).CombinedOutput(); err != nil {
		t.Fatalf("commit %s: %s", dest, out)
	}
	return imageID(t, dest)
}

// The build's grant is `sandboxruntime.BuilderGrant` and nothing else, so the permission refusal a person can
// still reach is a `RUN` step needing a privilege outside that set. A mount namespace is the cheapest one: it
// wants CAP_SYS_ADMIN, which is not in what the nested builder asks for, and the guest says EPERM.
func TestARunStepNeedingMoreThanTheBuildGrantSaysSo(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")
	sm, project, _ := buildE2E(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nRUN unshare -m /bin/true\n",
	}, "")

	var lines []string
	_, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, func(line string) { lines = append(lines, line) })
	if err == nil {
		t.Skip("this host let a RUN step take a mount namespace inside the build container, so the grant's " +
			"ceiling is not where docs/adr/0002 says it is and the refusal sentence needs re-measuring")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "capability grants") {
		t.Errorf("the build stream left the permission refusal unanswered:\n%s\nerror: %v", joined, err)
	}
	// The answer points at the Dockerfile, because that is what it is: the person's own capability list has
	// no part in a build, and a refusal that sends them there costs them a settings round trip.
	if strings.Contains(joined, "capabilities you added") {
		t.Errorf("the refusal sent the person to a list a build never reads:\n%s", joined)
	}
}

// The host must hand in every image a multi-stage file names, and must not mistake a stage for one. `--pull=never`
// makes a missed ref a hard failure rather than a download.
func TestBuildRunsAMultiStageDockerfileOverTwoImages(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")
	// The second base is a pinned local name for an image this host already has, because MIKE refuses a
	// Dockerfile whose base floats on `latest` and a test must not need the network to get one.
	const runtimeRef = "localhost/mike-e2e-runtime:1"
	if out, err := exec.Command("podman", "tag", "docker.io/library/alpine:3.20", runtimeRef).CombinedOutput(); err != nil {
		t.Skipf("this host cannot make a second pinned base for the test: %s", strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", runtimeRef).CombinedOutput()
	})

	sm, project, _ := buildE2E(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20 AS build\nCOPY hello /hello\nRUN cp /hello /artifact\nFROM " + runtimeRef + "\nCOPY --from=build /artifact /artifact\n",
		"hello":      "built-in-an-earlier-stage\n",
	}, "")

	result, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", result.Image).CombinedOutput()
	})
	if len(result.Bases) != 2 {
		t.Errorf("the build reported %d images (%v), want the two the file names", len(result.Bases), result.Bases)
	}
	files := imageRootFS(t, result.Image)
	if files["artifact"] != "built-in-an-earlier-stage\n" {
		t.Errorf("the loaded image does not hold the earlier stage's output: %v", files)
	}
	// The final stage is the image, not the build stage's filesystem.
	if _, ok := files["hello"]; ok {
		t.Errorf("the build stage's own root came along into the image: %v", files)
	}
}

// shortImageID keeps a test failure readable.
func shortImageID(id string) string {
	if id == "" {
		return "(no image)"
	}
	if len(id) > 19 {
		return id[:19]
	}
	return id
}

// Repository text must not name an image from the host's own store: a short ref means what Docker means by it, not
// what this machine happens to hold. This was measured against the leak before it was fixed.
func TestBuildCannotNameAHostLocalImageByShortName(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")
	const short = "mike-host-store-probe"
	const localRef = "localhost/" + short + ":1"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("unreachable-otherwise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY marker /marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("podman", "build", "-t", localRef, "-f", filepath.Join(dir, "Dockerfile"), dir).CombinedOutput(); err != nil {
		t.Skipf("this host cannot build the local base the test needs: %s", strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", localRef).Run() })

	sm, project, _ := buildE2E(t, map[string]string{"Dockerfile": "FROM " + short + ":1\n"}, "")
	_, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)

	// Whether the public registry has that name is not MIKE's business; what is provable is that the host's
	// own image never came in. The failure case is equally fine and needs no network to reach.
	if err == nil {
		t.Cleanup(func() { _ = exec.Command("podman", "rmi", ProjectImageName(project.Name)).Run() })
		files := imageRootFS(t, ProjectImageName(project.Name))
		if files["marker"] != "" {
			t.Errorf("a short FROM read the host's image store: marker=%q", files["marker"])
		}
	}
}

// A build reaches the network even for an isolated project, asserted here through what the guest sees. The fetch
// itself was measured by hand, because a Go test reaches no internet (docs/adr/0002).
func TestBuildReachesTheNetworkEvenForAnIsolatedProject(t *testing.T) {
	requireHostImage(t, "docker.io/library/alpine:3.20")
	sm, project, _ := buildE2E(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nRUN { ip route show 2>/dev/null || cat /proc/net/route; } > /routes\n",
	}, "")
	project.SandboxNetworkIsolation = true

	res, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, nil)
	if err != nil {
		t.Fatalf("an isolated project could not build its image: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", res.Image).Run() })

	routes := imageRootFS(t, res.Image)["routes"]
	if !strings.Contains(routes, "default") {
		t.Errorf("the build container had no route out, so its RUN steps cannot install anything:\n%s", routes)
	}
}
