package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"mike/backend/types"
	"mike/pkg/logging"
	"mike/pkg/sandboxruntime"
)

// A project's Dockerfile is built in a one-shot gVisor container over a staged subtree of one commit; the image is
// pushed out as an archive the host loads. Rules and measurements: docs/adr/0002.

// Where the guest sees each directory. The names are absolute, fixed and namespaced so nothing in a
// project's own mount list can collide with them.
const (
	buildContextMount = "/mike-ctx"
	buildOutputMount  = sandboxruntime.BuildOutputMount
	buildBaseMount    = "/mike-base"
	// buildStoreRoot holds buildah's own store, and must sit on the container's root filesystem: a `vfs` layer
	// written to a guest tmpfs fails its own extended-attribute read. The builder's temp directory is the other
	// case, and sandboxruntime.BuildTempDir says why.
	buildStoreRoot = "/var/tmp/mike-build"
	// buildImageTag is the name the finished image carries inside the guest and in the archive.
	buildImageTag = "mike-build:latest"
)

// The build labels ride on the image itself, so a cache hit or an audit answer survives a restart with no table of
// MIKE's.
const (
	buildCacheKeyLabel = "mike.build-cache-key"
	buildCommitLabel   = "mike.commit"
	// buildBaseLabel records which base the bytes were built on, which the cache key cannot say: a ref like
	// `alpine:3.20` can move.
	buildBaseLabel = "mike.base-image"
)

// The builder's capabilities are `sandboxruntime.BuilderGrant`, shared with the probe so the two cannot drift.
// They are not the project's: the grant is not monotone, and a set with one name missing from it breaks builds
// that the whole set completes (docs/adr/0002).

// BuildResult is what a build produced: the audit line reports it and the tests assert it. Nothing decides from
// it.
type BuildResult struct {
	Image  string `json:"image"`
	Commit string `json:"commit"`
	From   string `json:"from"`
	// Bases names every image the Dockerfile asked for, which is what the host fetched and handed in.
	Bases       []string `json:"bases,omitempty"`
	CacheKey    string   `json:"cache_key"`
	Files       int      `json:"files"`
	ScratchPeak string   `json:"scratch_peak,omitempty"`
	// BaseImageID is the id of each image the host handed in, in file order — the bytes a floating tag names
	// only on the day it was fetched.
	BaseImageID string `json:"base_image_id,omitempty"`
}

// buildPlan is a build that has been read, checked and staged but not run. Anything that can be refused without a
// container is refused here.
type buildPlan struct {
	project       *types.Project
	scratch       *buildScratch
	dockerfileRel string
	contextRoot   string
	commit        string
	treeOID       string
	fromRef       string
	baseArchives  []string
	file          *BuildFile
	cacheKey      string
	containerName string
	stagedFiles   int
	stagedBytes   int64
	// baseNames[i] holds every name the guest's store must answer for archive i, so any spelling the Dockerfile
	// used resolves. An unlisted spelling fails on `--pull=never` rather than downloading.
	baseNames    [][]string
	baseImageIDs []string
}

// ContextDir is the staged tree inside the scratch directory.
func (p *buildPlan) ContextDir() string { return p.scratch.ContextDir() }

// baseImageLabel joins the base ids the way the `mike.base-image` label and the build result both name them.
// Empty means no base's id could be read.
func (p *buildPlan) baseImageLabel() string { return strings.Join(p.baseImageIDs, ",") }

// planProjectBuild reads, checks and stages everything a build needs, so a Dockerfile that cannot work fails by
// line and path rather than three minutes into a container.
func (sm *SandboxManager) planProjectBuild(ctx context.Context, project *types.Project, emit func(string)) (*buildPlan, error) {
	if project == nil {
		return nil, fmt.Errorf("no project")
	}
	if !sm.available {
		return nil, fmt.Errorf("gVisor is not available, so this project cannot be built or run in a sandbox")
	}

	dockerfileRel, err := dockerfileInRepo(project.Path, project.SandboxDockerfilePath)
	if err != nil {
		return nil, err
	}
	contextRoot, err := BuildContextRoot(dockerfileRel, project.SandboxBuildContext)
	if err != nil {
		return nil, err
	}

	scratch, err := newBuildScratch(project.ID, project.Name)
	if err != nil {
		return nil, err
	}
	plan := &buildPlan{project: project, scratch: scratch, dockerfileRel: dockerfileRel, contextRoot: contextRoot}

	emit(fmt.Sprintf("staging the committed files under %s as the build context…", contextRoot))
	staged, err := StageBuildContext(ctx, project, contextRoot, dockerfileRel, scratch.ContextDir())
	if err != nil {
		scratch.Remove()
		return nil, err
	}
	plan.commit = staged.Commit
	emit(fmt.Sprintf("context %s: %d files, %s from commit %s", contextRoot, staged.Files, humanBytes(staged.Bytes), shortCommit(staged.Commit)))

	treeOID, err := contextTreeOID(ctx, project.Path, staged.Commit, contextRoot)
	if err != nil {
		scratch.Remove()
		return nil, fmt.Errorf("the build context has no tree to hash: %w", err)
	}
	plan.treeOID = treeOID
	plan.stagedFiles = staged.Files
	plan.stagedBytes = staged.Bytes

	content, err := CommitFile(ctx, project.Path, staged.Commit, dockerfileRel)
	if err != nil {
		scratch.Remove()
		return nil, err
	}
	file, err := ParseBuildFile(string(content))
	if err != nil {
		scratch.Remove()
		return nil, fmt.Errorf("%s at %s: %w", dockerfileRel, staged.Commit[:min(len(staged.Commit), 12)], err)
	}
	plan.file = file

	for _, ref := range file.Bases {
		if err := validateBuildBase(ref); err != nil {
			scratch.Remove()
			return nil, err
		}
	}
	plan.fromRef = file.FirstFrom

	if err := checkBuildMounts(project); err != nil {
		scratch.Remove()
		return nil, err
	}

	plan.cacheKey = buildCacheKey(staged.Commit, plan.treeOID, string(content), file.Bases)
	plan.containerName = sandboxruntime.BuildContainerName(project.ID, scratch.ID)
	return plan, nil
}

// contextTreeOID is the content hash of the context subtree. A commit's own tree has no path spelling, so the root
// is asked for as `<commit>^{tree}`.
func contextTreeOID(ctx context.Context, repo, commit, root string) (string, error) {
	spec := commit + ":" + root
	if root == "." {
		spec = commit + "^{tree}"
	}
	return resolveGitRef(ctx, repo, "rev-parse", spec)
}

// validateBuildBase checks a base named by repository text for shape only: the ref reaches a podman argument, so
// it must not be able to become one. What it names is the file's business, and `mike.base-image` records the bytes
// a floating tag cannot name.
func validateBuildBase(ref string) error {
	// The host expands Docker's short names later (stageBaseImageArchives), so the rule that qualifies a typed
	// setting does not apply to a repository's file.
	if err := sandboxruntime.ValidateImageRef(ref); err != nil {
		return fmt.Errorf("an image the Dockerfile names: %w", err)
	}
	return nil
}

func checkBuildMounts(project *types.Project) error {
	for _, m := range project.SandboxMounts {
		if m.HostPath == "" {
			continue
		}
		if _, err := os.Stat(m.HostPath); err != nil {
			return fmt.Errorf("the configured mount %s does not exist on this host — create it, or remove the mount; an image build will not create host directories", m.HostPath)
		}
	}
	return nil
}

// buildCacheKey is the commit, the context tree, the Dockerfile's bytes and the images it names. The bytes are
// hashed because MIKE models no instruction and an unmodelled one still changes the image.
func buildCacheKey(commit, treeOID, dockerfile string, bases []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", commit, treeOID, dockerfile)
	for _, b := range bases {
		fmt.Fprintf(h, "base\x00%s\n", b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	if commit == "" {
		return "…"
	}
	return commit
}

// projectBuildKey computes the cache key from the commit alone, so a cache hit is answered before any build
// directory exists.
func (sm *SandboxManager) projectBuildKey(ctx context.Context, project *types.Project) (string, error) {
	dockerfileRel, err := dockerfileInRepo(project.Path, project.SandboxDockerfilePath)
	if err != nil {
		return "", err
	}
	contextRoot, err := BuildContextRoot(dockerfileRel, project.SandboxBuildContext)
	if err != nil {
		return "", err
	}
	commit, err := resolveGitRef(ctx, project.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if err := validateGitObjectID(commit); err != nil {
		return "", err
	}
	treeOID, err := contextTreeOID(ctx, project.Path, commit, contextRoot)
	if err != nil {
		return "", fmt.Errorf("the build context %s is not in commit %s: %w", contextRoot, shortCommit(commit), err)
	}
	content, err := CommitFile(ctx, project.Path, commit, dockerfileRel)
	if err != nil {
		return "", err
	}
	file, err := ParseBuildFile(string(content))
	if err != nil {
		return "", err
	}
	return buildCacheKey(commit, treeOID, string(content), file.Bases), nil
}

// BuildProjectImage builds the project's sandbox image, waiting for a build or pull of that image already
// running and reporting its result instead of starting a second one. onProgress receives the lines of
// whichever build ends up running, so a request that joined late still reads the build it waited on.
func (sm *SandboxManager) BuildProjectImage(ctx context.Context, project *types.Project, onProgress func(line string)) (*BuildResult, error) {
	image := sm.rt.ResolveImageRef(ResolveProjectImage(project).Image)
	report := func(status types.SandboxImageStatus) {
		if onProgress != nil && status.Line != "" {
			onProgress(status.Line)
		}
	}

	for {
		if op, joined := sm.joinImageOp(image, report); joined {
			if onProgress != nil {
				onProgress("waiting for the sandbox image build already running…")
			}
			result, err := op.await()
			if err != nil || result != nil {
				return result, err
			}
			// What finished was a pull, and a build was asked for: run it now that the key is free.
			continue
		}
		op, claimed := sm.claimImageOp(image, report)
		if !claimed {
			continue
		}
		return sm.runImageOp(image, op, func() (*BuildResult, error) {
			op.announce(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding})
			return sm.buildProjectImageDirect(ctx, project, func(line string) {
				op.progress(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Line: line})
			})
		})
	}
}

// buildProjectImageDirect runs one build of the project's image. It neither waits for a build in flight nor
// publishes its own, so only the caller holding that image's key may call it.
//
// A failure is logged as well as returned: the stream's last line is all the panel keeps, so without the log
// entry a failed build leaves nothing to grep by.
func (sm *SandboxManager) buildProjectImageDirect(ctx context.Context, project *types.Project, onProgress func(line string)) (result *BuildResult, err error) {
	emit := func(line string) {
		if onProgress != nil {
			onProgress(line)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var plan *buildPlan
	defer func() {
		if err == nil {
			return
		}
		entry := logging.For(logging.Sandbox).With(logging.ProjectID, project.ID)
		if plan != nil {
			entry = entry.With(logging.BuildID, plan.scratch.ID)
		}
		// A build whose stream went away was stopped by the person watching it, not by a fault.
		if ctx.Err() != nil {
			entry.Warnf("image build of %s stopped before it answered: %v", ProjectImageName(project.Name), err)
			return
		}
		entry.Errorf("image build of %s failed: %v", ProjectImageName(project.Name), err)
	}()

	plan, err = sm.planProjectBuild(ctx, project, emit)
	if err != nil {
		return nil, err
	}
	defer plan.scratch.Remove()
	defer registerBuild(plan.scratch.ID, cancel)()
	// The budget is taken before any work, so a build that never gets room has staged nothing and started nothing.
	releaseMemory, err := reserveBuildMemory(ctx, project, emit)
	if err != nil {
		return nil, err
	}
	defer releaseMemory()

	if err := sm.ensureBuilderImage(ctx, emit); err != nil {
		return nil, err
	}
	builderVersion := sm.builderVersion(sandboxruntime.BuilderImageDefault)

	result = &BuildResult{
		Image:    ProjectImageName(project.Name),
		Commit:   plan.commit,
		From:     plan.fromRef,
		Bases:    append([]string{}, plan.file.Bases...),
		CacheKey: plan.cacheKey,
		Files:    plan.stagedFiles,
	}

	logging.For(logging.Sandbox).With(logging.ProjectID, project.ID).With(logging.BuildID, plan.scratch.ID).Infof(
		"building %s from commit %s: context=%s files=%d bytes=%d from=%s bases=%d network=open mounts=%d builder=%s buildah=%s",
		result.Image, shortCommit(plan.commit), plan.contextRoot, plan.stagedFiles, plan.stagedBytes,
		plan.fromRef, len(plan.file.Bases), len(project.SandboxMounts), sandboxruntime.BuilderImageDefault, builderVersion)

	if err := sm.stageBaseImageArchives(ctx, plan, emit); err != nil {
		return nil, err
	}
	result.BaseImageID = plan.baseImageLabel()
	if err := sm.runBuildContainer(ctx, plan, emit); err != nil {
		return nil, err
	}
	if err := sm.loadBuiltImage(plan, result, emit); err != nil {
		return nil, err
	}
	result.ScratchPeak = humanBytes(plan.scratch.peak.Load())
	return result, nil
}

// ensureBuilderImage fetches the nested builder image into the host's image store, because the build container
// is created with `--pull never` and cannot fetch it itself.
func (sm *SandboxManager) ensureBuilderImage(ctx context.Context, emit func(string)) error {
	ref := sandboxruntime.BuilderImageDefault
	if sm.ImageReady(ref) {
		return nil
	}
	emit(fmt.Sprintf("pulling the nested builder image %s (once per host)…", ref))
	if err := sm.PullImageWithProgress(ref, emit); err != nil {
		return fmt.Errorf("pull the nested builder image %s: %w", ref, err)
	}
	return nil
}

// buildTailLines bounds how much guest output is kept to read a cause from. A failing buildah names its reason in
// one line.
const buildTailLines = 80

// buildFailureHint answers a permission refusal from the guest, whose own lines the caller has already
// emitted. A build's grant is not a value a person set, so this points at the two things that can still cause
// one rather than at a list worth editing (the measured set and its failures: docs/adr/0002).
func buildFailureHint(lines []string) string {
	permissionDenied := false
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "setting capabilities") ||
			strings.Contains(lower, "uid_map") || strings.Contains(lower, "gid_map") {
			permissionDenied = true
			break
		}
	}
	if !permissionDenied {
		return ""
	}
	return fmt.Sprintf("the build failed on a permission check. A build container runs with MIKE's own fixed set of "+
		"%d capability grants and reads none of this project's Capabilities, so editing that list changes nothing "+
		"here. What is left is the Dockerfile: a RUN step needing a privilege outside that set, or a base image "+
		"whose layers need one the set does not carry.", len(sandboxruntime.BuilderGrant))
}

// stageBaseImageArchives writes every image the Dockerfile named into the scratch directory, tagged in the guest
// with each spelling the file used, because the build container downloads nothing.
func (sm *SandboxManager) stageBaseImageArchives(ctx context.Context, plan *buildPlan, emit func(string)) error {
	for i, raw := range plan.file.Bases {
		// Docker's qualification only, never `ResolveImageRef`: that prefers the host's image store, and a host image
		// is bytes no container can reach. A local base is spelled `localhost/…`, which a person reviews.
		ref := sandboxruntime.QualifyDockerImageRef(raw)
		if !sm.ImageReady(ref) {
			emit(fmt.Sprintf("pulling %s for the build to be handed…", ref))
			if err := sm.PullImageWithProgress(ref, emit); err != nil {
				return fmt.Errorf("pull the Dockerfile's image %s: %w", ref, err)
			}
		}
		// No ref validation here: `validateBuildBase` checked every ref the file named at plan time, and
		// qualification cannot make an invalid ref valid.
		archive := plan.scratch.BaseArchiveAt(i)
		emit(fmt.Sprintf("handing %s to the build container…", ref))
		if _, err := sm.podmanExec("save", "--format", "docker-archive", "-o", archive, ref); err != nil {
			return fmt.Errorf("write the image archive for %s: %w", ref, err)
		}
		plan.baseArchives = append(plan.baseArchives, archive)
		names := []string{ref}
		if spelled := strings.TrimSpace(raw); spelled != "" && spelled != ref {
			names = append(names, spelled)
		}
		plan.baseNames = append(plan.baseNames, names)
		if id, idErr := sm.imageIDOf(ref); idErr == nil {
			plan.baseImageIDs = append(plan.baseImageIDs, id)
		} else {
			logging.For(logging.Sandbox).With(logging.ProjectID, plan.project.ID).Warnf("image %s id could not be read, so the build record will not name it: %v", ref, idErr)
		}
	}
	return nil
}

// runBuildContainer starts the one-shot build container, streams its output and removes it on every exit:
// success, refusal, cancellation or the size-cap stop.
func (sm *SandboxManager) runBuildContainer(ctx context.Context, plan *buildPlan, emit func(string)) error {
	args, err := sm.buildContainerArgs(plan)
	if err != nil {
		return err
	}
	name := plan.containerName

	out, err := sm.rt.PodmanExecContext(ctx, args...)
	if err != nil {
		// podman names the cause in its own sentence; an exit status alone does not.
		return fmt.Errorf("create the build container: %w: %s", err, lastLine(string(out)))
	}
	// One teardown path, on every exit: a build container is single-use (its layers live in its own
	// writable layer) and a leftover is invisible except by its name, which carries the build id.
	defer func() {
		sm.stopBuildContainer(name)
		if err := sm.rt.RemoveContainer(name); err != nil {
			logging.For(logging.Sandbox).With(logging.BuildID, plan.scratch.ID).Warnf("build container %s left behind: %v", name, err)
		}
	}()

	plan.scratch.Watch(buildOutputMaxBytes, func() { sm.stopBuildContainer(name) })
	defer plan.scratch.StopWatch()

	emit("building in a gVisor container (read-only inputs, network for the build's own steps)…")
	// The guest's own lines, kept to a bound, because the only evidence about why a nested build failed is
	// what it printed (buildFailureHint reads it for a permission refusal).
	var tail []string
	capture := func(line string) {
		if len(tail) < buildTailLines {
			tail = append(tail, line)
		}
		emit(line)
	}
	err = sm.streamPodman(
		podmanCommand(append(sandboxruntime.RuntimeFlagsForExistingContainer(), "start", "-a", name)...),
		"build", plan.fromRef, "build", capture)
	if err != nil {
		if hint := buildFailureHint(tail); hint != "" {
			emit(hint)
			return fmt.Errorf("build %s: %w — %s", plan.fromRef, err, hint)
		}
		return fmt.Errorf("build %s: %w", plan.fromRef, err)
	}
	if _, statErr := os.Stat(plan.scratch.Archive()); statErr != nil {
		return fmt.Errorf("the build finished without producing an image archive: %w", statErr)
	}
	return nil
}

// buildContainerArgs is the whole podman argument list for one build: gVisor with the build's fixed grant,
// the project's inherited mounts read-only, a network, no published ports.
func (sm *SandboxManager) buildContainerArgs(plan *buildPlan) ([]string, error) {
	project := plan.project
	// Only what this function reads is taken from the project's configuration; task-only fields are left alone
	// rather than filled with values that would do nothing.
	opts := ProjectOpts(project)

	args := sandboxruntime.GVisorCreateArgs(plan.containerName)

	args, err := sandboxruntime.AppendCapabilities(args, sandboxruntime.BuilderGrant)
	if err != nil {
		return nil, err
	}

	envArgs, err := containerEnvArgs(plan.scratch.ID, opts)
	if err != nil {
		return nil, err
	}
	args = append(args, envArgs...)

	// Inherited mounts, read-only: a mount row grants a task and its reviewed code, not a Dockerfile nobody has
	// read.
	for _, m := range opts.SandboxMounts {
		if m.HostPath == "" || m.ContainerPath == "" {
			continue
		}
		if err := sandboxruntime.ValidateContainerPath(m.ContainerPath); err != nil {
			return nil, err
		}
		args = append(args, "-v", sandboxruntime.BindMountSpec(m.HostPath, m.ContainerPath, "ro"))
	}

	// The three MIKE mounts: inputs that cannot be written, and one directory that can.
	args = append(args, "-v", sandboxruntime.BindMountSpec(plan.ContextDir(), buildContextMount, "ro"))
	args = append(args, "-v", sandboxruntime.BindMountSpec(plan.scratch.Output, buildOutputMount, "rw"))
	if len(plan.baseArchives) > 0 {
		args = append(args, "-v", sandboxruntime.BindMountSpec(filepath.Dir(plan.baseArchives[0]), buildBaseMount, "ro"))
	}

	if opts.MemoryLimit > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dM", opts.MemoryLimit))
	}
	// A build always gets a network: `sandbox_network_isolation` decides for the agent, not for a build. Its
	// inputs arrive as a bind mount, and there is no route to the host's loopback.

	return append(args, sandboxruntime.BuilderImageDefault, "/bin/sh", "-c", buildContainerScript(plan)), nil
}

// buildContainerScript is the guest side as one shell line: read the base image in, run the Dockerfile under the
// chroot isolation the probe measured, push the result out. `--pull=never` keeps registry credentials on the host.
func buildContainerScript(plan *buildPlan) string {
	store := fmt.Sprintf("buildah --root %s/s --runroot %s/r --storage-driver vfs", buildStoreRoot, buildStoreRoot)
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\n")
	// The host makes this directory and the guest never creates one, so a missing bind fails naming the path it
	// could not find instead of building on the root filesystem. The export follows the project's own
	// environment, which `--env` has already applied (sandboxruntime.BuildTempDir).
	fmt.Fprintf(&b, "export TMPDIR=%s\n", sandboxruntime.BuildTempDir)
	fmt.Fprintf(&b, "mkdir -p %s/s %s/r\n", buildStoreRoot, buildStoreRoot)
	for i, archive := range plan.baseArchives {
		// The guest is told only the archive's name and takes the id from the pull: with several images in the
		// store, "the first one" names nothing.
		fmt.Fprintf(&b, "id=$(%s pull docker-archive:%s/%s | tail -1)\n", store, buildBaseMount, filepath.Base(archive))
		for _, name := range plan.baseNames[i] {
			fmt.Fprintf(&b, "%s tag \"$id\" %s > /dev/null\n", store, sandboxruntime.ShellQuote(name))
		}
	}
	pairs := [][2]string{
		{buildCacheKeyLabel, plan.cacheKey},
		{buildCommitLabel, plan.commit},
	}
	if label := plan.baseImageLabel(); label != "" {
		pairs = append(pairs, [2]string{buildBaseLabel, label})
	}
	labels := ""
	for _, l := range pairs {
		labels += " --label " + sandboxruntime.ShellQuote(l[0]+"="+l[1])
	}
	// The flags precede the context: `bud <context> -f <dockerfile>` is rejected with "accepts at most 1
	// arg", which reads like a malformed Dockerfile and is a flag-ordering rule.
	fmt.Fprintf(&b, "%s bud --isolation chroot --pull=never --format oci%s -t %s -f %s %s\n",
		store, labels, buildImageTag,
		sandboxruntime.ShellQuote(path.Join(buildContextMount, plan.dockerfileInContext())), buildContextMount)
	fmt.Fprintf(&b, "%s push %s docker-archive:%s/image.tar\n", store, buildImageTag, buildOutputMount)
	return b.String()
}

// dockerfileInContext is the Dockerfile's path inside the staged tree.
func (p *buildPlan) dockerfileInContext() string {
	if p.contextRoot == "." {
		return p.dockerfileRel
	}
	rel, err := relativeToContext(p.contextRoot, p.dockerfileRel)
	if err != nil {
		return p.dockerfileRel
	}
	return rel
}

// loadBuiltImage loads the pushed archive into the host's store and only then retires the previous image, so a
// failed or truncated build leaves the project with the image it had.
func (sm *SandboxManager) loadBuiltImage(plan *buildPlan, result *BuildResult, emit func(string)) error {
	if emit == nil {
		emit = func(string) {}
	}
	archive := plan.scratch.Archive()
	info, err := os.Stat(archive)
	if err != nil {
		return fmt.Errorf("the build produced no image archive: %w", err)
	}
	emit(fmt.Sprintf("loading the built image (%s)…", humanBytes(info.Size())))
	out, err := sm.podmanExec("load", "-i", archive)
	if err != nil {
		return fmt.Errorf("load the built image: %w: %s", err, lastLine(string(out)))
	}
	loaded, err := loadedImageName(string(out))
	if err != nil {
		return err
	}
	if err := sandboxruntime.ValidateImageRef(loaded); err != nil {
		return fmt.Errorf("the build produced %q, which is not a usable image name: %w", loaded, err)
	}

	// The build record is read off the loaded image **before** the project's name is moved onto it, because that
	// name is a claim the project can boot from.
	key, err := sm.imageLabel(loaded, buildCacheKeyLabel)
	if err != nil || key == "" {
		return fmt.Errorf("the built image carries no build record: %v", err)
	}
	if key != plan.cacheKey {
		return fmt.Errorf("the built image is labelled for a different build (%s); refusing to use it", key)
	}

	previous, _ := sm.podmanExec("images", "-q", result.Image)
	hasPrevious := strings.TrimSpace(string(previous)) != ""

	if _, err := sm.podmanExec("tag", loaded, result.Image); err != nil {
		return fmt.Errorf("tag the built image %s: %w", result.Image, err)
	}
	loadedID, err := sm.imageIDOf(result.Image)
	if err != nil {
		return fmt.Errorf("the retagged image %s cannot be read back: %w", result.Image, err)
	}
	// The builder's own tag is an intermediate, dropped by name rather than by id: the loaded id is what the
	// project boots from now.
	if _, err := sm.podmanExec("untag", loadedID, loaded); err != nil {
		logging.For(logging.Sandbox).With(logging.BuildID, plan.scratch.ID).Warnf("intermediate name %s left on image %s: %v", loaded, loadedID, err)
	}
	// The replaced image is retired by id, and skipped when that id is the one that just landed.
	if retired := strings.TrimSpace(string(previous)); hasPrevious && retired != "" && !sameImageID(retired, loadedID) {
		if _, err := sm.podmanExec("rmi", retired); err != nil {
			logging.For(logging.Sandbox).With(logging.ProjectID, plan.project.ID).Warnf("previous project image %s could not be removed: %v", result.Image, err)
		}
	}
	// Every removal above may fail, so the caller's one guarantee is checked here: the project's name resolves to
	// the image this build produced.
	if settled, idErr := sm.imageIDOf(result.Image); idErr != nil || settled != loadedID {
		return fmt.Errorf("the project image %s does not resolve to the image just built (%s): %v", result.Image, loadedID, idErr)
	}
	emit(fmt.Sprintf("image %s ready", result.Image))
	return nil
}

// sameImageID compares two ids podman printed: `images -q` writes a bare hex id and `image inspect` prefixes it
// with the algorithm.
func sameImageID(a, b string) bool {
	trim := func(id string) string {
		id = strings.ToLower(strings.TrimSpace(id))
		if i := strings.LastIndex(id, ":"); i >= 0 && strings.HasPrefix(strings.ToLower(id), "sha256:") {
			return id[i+1:]
		}
		return id
	}
	x, y := trim(a), trim(b)
	return x != "" && y != "" && (x == y || strings.HasPrefix(x, y) || strings.HasPrefix(y, x))
}

// imageIDOf is the id one name currently resolves to, the identity a retirement decision is made on.
func (sm *SandboxManager) imageIDOf(image string) (string, error) {
	out, err := sm.podmanExec("image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// builderVersion is the nested builder's own version as its image records it. The builder ref floats, so a
// build's own log line says which builder ran; an image with no such label reads as unknown rather than costing
// the build.
func (sm *SandboxManager) builderVersion(image string) string {
	version, err := sm.imageLabel(image, "io.buildah.version")
	if err != nil || version == "" {
		return "unknown"
	}
	return version
}

// loadedImageName reads the name out of `podman load`'s own report rather than guessing at one the archive chose.
func loadedImageName(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(line), "Loaded image: "); ok {
			return strings.TrimSpace(ref), nil
		}
		if ref, ok := strings.CutPrefix(strings.TrimSpace(line), "Loaded image ID: "); ok {
			return "", fmt.Errorf("the archive carried no image name (%s); the build cannot be tagged", ref)
		}
	}
	return "", fmt.Errorf("could not tell what the archive loaded: %s", lastLine(out))
}

// imageLabel reads one label off an image, or an error when the image or the label is missing.
func (sm *SandboxManager) imageLabel(image, label string) (string, error) {
	out, err := sm.podmanExec("image", "inspect", "--format", "{{ index .Config.Labels \""+label+"\" }}", image)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(out))
	if value == "<no value>" || value == "map[]" {
		return "", fmt.Errorf("image %s has no %s label", image, label)
	}
	return value, nil
}

// stopBuildContainer stops a build container by name. The runtime flags a gVisor container was created
// with have to be repeated on the stop, exactly as on the start.
func (sm *SandboxManager) stopBuildContainer(name string) {
	args := append(sandboxruntime.RuntimeFlagsForExistingContainer(), "stop", "--time", "5", name)
	if _, err := sm.podmanExec(args...); err != nil {
		logging.For(logging.Sandbox).With(logging.BuildID, name).Warnf("stopping build container %s: %v", name, err)
	}
}

// ImageCarriesBuild reports whether the image on the host is this project's build of this key.
func (sm *SandboxManager) ImageCarriesBuild(image, cacheKey string) bool {
	if cacheKey == "" {
		return false
	}
	value, err := sm.imageLabel(image, buildCacheKeyLabel)
	return err == nil && value == cacheKey
}

// activeBuilds are the builds in flight, so shutdown can reach all of them at once.
var activeBuilds = struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}{}

// registerBuild records a build's cancel under its scratch id and returns the function that forgets it.
func registerBuild(id string, cancel context.CancelFunc) func() {
	activeBuilds.mu.Lock()
	if activeBuilds.cancels == nil {
		activeBuilds.cancels = map[string]context.CancelFunc{}
	}
	activeBuilds.cancels[id] = cancel
	activeBuilds.mu.Unlock()
	return func() {
		activeBuilds.mu.Lock()
		delete(activeBuilds.cancels, id)
		activeBuilds.mu.Unlock()
	}
}

// CancelBuilds cancels every in-flight build, which kills its container: only a cancelled context bounds the
// child. Shutdown runs it before containers are removed.
func CancelBuilds() {
	activeBuilds.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(activeBuilds.cancels))
	for _, c := range activeBuilds.cancels {
		cancels = append(cancels, c)
	}
	activeBuilds.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// ActiveBuildCount reports how many builds are running. Its only caller is a test, which uses it to see that a
// build registered its cancel and released it.
func ActiveBuildCount() int {
	activeBuilds.mu.Lock()
	defer activeBuilds.mu.Unlock()
	return len(activeBuilds.cancels)
}
