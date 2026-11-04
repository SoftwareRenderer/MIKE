package sandboxruntime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"mike/pkg/logging"
)

// The build probe: ProbeSandbox answers "did a gVisor container run", this answers whether such a container can
// build an image under runsc, and a host can pass the first and fail the second. A failed stage is an answer
// rather than a fault, so the report keeps every stage's reason instead of stopping at the first.

const (
	// BuilderImageDefault ships buildah and a store that needs no device the guest does not have.
	BuilderImageDefault = "quay.io/buildah/stable:latest"

	// buildProbeRunBase names its registry and pins a tag: the stage that uses it pulls it from
	// inside the guest, which is the thing being measured.
	buildProbeRunBase = "docker.io/library/alpine:3.20"

	// buildProbeStorageDriver is vfs, measured: `overlay` inside the guest fails at its first mount
	// with "transport endpoint is not connected", and fuse-overlayfs would need /dev/fuse, which is
	// neither a mount nor a capability in this package.
	buildProbeStorageDriver = "vfs"

	// buildProbeStoreDir is on the container's rootfs, not in /tmp, and that is a measured difference
	// rather than a preference: /tmp in the guest is tmpfs, and committing a vfs store that lives there
	// fails when the copier reads a layer's xattrs. The rootfs is the overlay gVisor itself mounted.
	buildProbeStoreDir = "/var/tmp/mike-build-probe"

	// buildProbeContainerPrefix keeps these containers outside the name families the boot sweep and
	// the e2e teardown delete, the same way the sandbox probe's own name does.
	buildProbeContainerPrefix = "mike-build-probe"

	// StaleBuildProbeFilter is podman's name filter for the containers an interrupted build probe left
	// behind. That family is deliberately outside the boot sweep's families — a probe must not be able
	// to delete a live sandbox — so the probe command sweeps its own family, and nothing else does.
	StaleBuildProbeFilter = buildProbeContainerPrefix + "-"

	// buildProbeContainerCommand holds the container up so the context can be staged into it and the
	// build run against it; GVisorCreateArgs clears the entrypoint, so the command is written here.
	buildProbeContainerCommand = "/bin/sh"

	// buildProbeStoreRoot and buildProbeRunRoot are inside the container, so nothing reaches the
	// host's container storage from inside the guest.
	buildProbeStoreRoot = buildProbeStoreDir + "/store"
	buildProbeRunRoot   = buildProbeStoreDir + "/run"

	// buildProbeContextPath is where the staged build context lands inside the container.
	buildProbeContextPath = buildProbeStoreDir + "/ctx"

	// buildProbePayloadName is the file the COPY-only stage copies, so that stage exercises the copy
	// pass and not only the metadata.
	buildProbePayloadName = "hello"

	// BuildProbeStageTimeout bounds one stage's build. A COPY-only vfs build is tens of seconds; a
	// stage that pulls a base image from inside the guest is minutes on a cold cache.
	BuildProbeStageTimeout = 5 * time.Minute

	// BuildProbeTimeout bounds the whole ladder, so a wedged podman costs a caller this much and no
	// more. Five stages at the per-stage ceiling is the arithmetic; it is not applied by anything in
	// this package — the caller binds it to a context.
	BuildProbeTimeout = 5*time.Minute + 4*BuildProbeStageTimeout
)

// BuildProbeStage is one rung of the ladder: the container's grant set, and the build text it runs.
type BuildProbeStage struct {
	// Name is both the report's label and part of the container name, so it stays in podman's
	// container-name charset: lowercase, digits, dashes.
	Name string
	// Caps go to AppendCapabilities, the only path to a grant, and an empty list is a real stage:
	// the one that asks whether buildah needs anything at all.
	Caps []string
	// Network leaves the container on the project's own network instead of `--network none`.
	Network bool
	// Dockerfile is the whole build input for the stage.
	Dockerfile string
	// MeasuresRun says the text contains a RUN step, which is the capability a nested builder would
	// buy: repository-authored commands executing inside the guest.
	MeasuresRun bool
	// Informational marks a stage whose failure the ladder does not report as a fault, because what it
	// measures is a fact about this host rather than something MIKE requires. The union grant is that
	// case: a project's own larger set can break a build the three-cap grant finishes, and knowing
	// whether it does here is the reason to run the stage.
	Informational bool
}

// BuildProbeResult is one stage's answer.
type BuildProbeResult struct {
	Stage         string   `json:"stage"`
	Caps          []string `json:"caps,omitempty"`
	Network       bool     `json:"network,omitzero"`
	MeasuresRun   bool     `json:"measures_run,omitzero"`
	OK            bool     `json:"ok"`
	Informational bool     `json:"informational,omitzero"`
	Reason        string   `json:"reason,omitempty"`
}

// BuildProbeReport is every stage that ran, in order.
type BuildProbeReport struct {
	Image   string             `json:"image"`
	Results []BuildProbeResult `json:"results"`
	Storage string             `json:"storage"`
}

// Passed reports whether a required stage built an image. An informational stage is not a pass and not
// a failure; it is a measurement of something MIKE does not depend on.
func (rep BuildProbeReport) Passed() bool {
	for _, res := range rep.Results {
		if res.OK && !res.Informational {
			return true
		}
	}
	return false
}

// RunsWithinGuest reports whether a RUN step executed inside the guest, which is the question
// a nested builder would answer and an interpreted build does not need to.
func (rep BuildProbeReport) RunsWithinGuest() bool {
	for _, res := range rep.Results {
		if res.OK && res.MeasuresRun && !res.Informational {
			return true
		}
	}
	return false
}

// BuildGrant is the measured minimum that builds a `COPY` image and runs a `RUN` step under runsc. A build
// container is granted `BuilderGrant`, which contains this; the probe still measures the minimum on its own,
// because "the minimum builds" and "the shipped set builds" are two answers.
// Capabilities here are not monotone, so a candidate set is tested whole rather than derived; the measured
// table is in docs/adr/0002.
var BuildGrant = []string{"SETUID", "SETGID", "SETFCAP"}

// BuilderGrant is the whole capability grant of one image build, and a build reads nothing else: the nested
// builder hands this set to every `RUN` step of the Dockerfile it executes, so a grant missing one of these
// names moves buildah onto the path that also raises `RLIMIT_NOFILE` and `RLIMIT_NPROC` past what a rootless
// gVisor guest may raise without `CAP_SYS_RESOURCE`, and the step dies on a permission check.
//
// It is a set, not a floor. Taking `CHOWN` out of it fails extracting a base layer at
// `lchown /etc/shadow`, and `BuildGrant` plus `SYS_ADMIN` alone fails at `setting up pivot dir` — so a grant
// assembled by hand, one name short of this, is worse than this one. A project's own capability list is a
// statement about the container the agent runs in, which a build is not. docs/adr/0002 holds the measurement.
var BuilderGrant = []string{
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "NET_RAW", "SETPCAP", "SETGID", "SETFCAP", "SETUID", "SYS_CHROOT",
}

// buildProbeOfflineStages asks whether the guest can build at all: first with nothing granted, then
// with the set a measured nested build needed. The second stage only matters if the first failed, and
// it is reported either way because "the grant is what was missing" is a different answer from
// "nothing works under runsc".
func buildProbeOfflineStages() []BuildProbeStage {
	// COPY-only, and `FROM scratch`, so the stage needs no image in the guest's own store and no
	// network: it measures storage, the copy pass and the commit, nothing else.
	copyOnly := "FROM scratch\nCOPY " + buildProbePayloadName + " /" + buildProbePayloadName + "\n"
	return []BuildProbeStage{
		{Name: "offline", Dockerfile: copyOnly},
		{Name: "offline-granted", Caps: BuildGrant, Dockerfile: copyOnly},
	}
}

// buildProbeRunStage is the decisive rung, and it runs only after a build worked offline: a base image
// pulled from inside the guest, then one command executed in it. It measures the shipped grant, so a green
// here is the product's own argv rather than a subset of it.
func buildProbeRunStage() BuildProbeStage {
	return BuildProbeStage{
		Name:    "run-step",
		Caps:    BuilderGrant,
		Network: true,
		// Two steps: one that proves a command ran, one whose output only survives if the layer it
		// wrote was committed into the image.
		Dockerfile:  buildProbeRunDockerfile,
		MeasuresRun: true,
	}
}

// buildProbeRunDockerfile pulls a base from inside the guest and runs two steps: one proving a command
// executed, one whose output only survives if the layer it wrote was committed into the image.
const buildProbeRunDockerfile = "FROM " + buildProbeRunBase + "\nRUN /bin/true\nRUN echo guest > /greet\n"

// buildProbeHeavyDockerfile is the same ladder with the work a real Dockerfile does and a two-line one
// never touches: chown over a tree the build did not create, mode bits, symlinks and hard links, and a
// timestamp. Every one of these is a place where a grant set that "should" be enough moves buildah onto
// the path where it writes files it does not own — the failure the three-cap grant is measured against.
const buildProbeHeavyDockerfile = "FROM " + buildProbeRunBase + "\n" +
	"COPY hello /hello\n" +
	"RUN mkdir -p /d/sub/deeper && cp /hello /d/sub/deeper/f && chown -R 100:100 /d && " +
	"chmod 0750 /d/sub && ln -s /d/sub/deeper/f /link && ln /d/sub/deeper/f /hard && " +
	"touch -d '2000-01-01 00:00' /d/sub/deeper/f && tar -cf /d.tar /d\n" +
	"WORKDIR /d\nUSER 100\nCMD [\"/bin/sh\"]\n"

// buildProbeGrantWithoutCHOWN is the recorded failing set: the shipped grant minus CHOWN. It is written as a
// subtraction so the pair stays readable as one experiment — two grants that differ by one name, where the
// larger-by-name-count question is not the point and the missing name is.
var buildProbeGrantWithoutCHOWN = func() []string {
	out := make([]string, 0, len(BuilderGrant)-1)
	for _, cap := range BuilderGrant {
		if cap != "CHOWN" {
			out = append(out, cap)
		}
	}
	return out
}()

// buildProbeHardeningStages runs the shipped grant against the work a real Dockerfile does, then measures what
// the same work does on the measured minimum, then the grants known to answer the non-monotonicity question.
// `grant-without-chown` is the interesting one: if it ever builds, the finding recorded in docs/adr/0002 about
// CHOWN has stopped being true of this host and that record owes an update.
func buildProbeHardeningStages() []BuildProbeStage {
	return []BuildProbeStage{
		{
			Name: "heavy-run", Caps: BuilderGrant, Network: true,
			Dockerfile: buildProbeHeavyDockerfile, MeasuresRun: true,
		},
		{
			Name: "minimum-heavy", Caps: BuildGrant, Network: true,
			Dockerfile: buildProbeHeavyDockerfile, MeasuresRun: true, Informational: true,
		},
		{
			Name: "grant-without-chown", Caps: buildProbeGrantWithoutCHOWN, Network: true,
			Dockerfile: buildProbeHeavyDockerfile, MeasuresRun: true, Informational: true,
		},
		{
			Name: "superset-15", Caps: buildProbeWideSuperset, Network: true,
			Dockerfile: buildProbeHeavyDockerfile, MeasuresRun: true, Informational: true,
		},
		{
			Name: "superset-no-chown", Caps: buildProbeWideSupersetWithoutCHOWN, Network: true,
			Dockerfile: buildProbeHeavyDockerfile, MeasuresRun: true, Informational: true,
		},
	}
}

// buildProbeWideSuperset is the fifteen-name set the grant was first found with: the recommended defaults
// plus SYS_ADMIN and SYS_RESOURCE, which is what a person reaches for when a container "needs more".
var buildProbeWideSuperset = []string{
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "SETPCAP", "SETGID", "SETFCAP", "SETUID", "SYS_CHROOT",
	"SYS_ADMIN", "SYS_RESOURCE",
}

// buildProbeWideSupersetWithoutCHOWN reproduces the failure recorded in docs/adr/0002, and the pair of
// stages around it is the finding: this set fails, the same set with CHOWN restored builds, and the
// recommended defaults build with or without it. So what breaks a build is not the size of the grant and
// not one name — it is the combination, which is exactly why a grant is tested whole.
var buildProbeWideSupersetWithoutCHOWN = func() []string {
	out := make([]string, 0, len(buildProbeWideSuperset)-1)
	for _, cap := range buildProbeWideSuperset {
		if cap != "CHOWN" {
			out = append(out, cap)
		}
	}
	return out
}()

// buildProbeCreateArgs is one stage's `podman create` line: the shared gVisor posture, the stage's
// grant set through the only function that appends a capability, and nothing else. No mount, no port,
// no environment — a stage that needed one of those would be measuring a container the product cannot
// make, since a build container is created by the same preface with the project's own configuration.
func buildProbeCreateArgs(name, image string, st BuildProbeStage) ([]string, error) {
	args := GVisorCreateArgs(name)
	var err error
	if len(st.Caps) > 0 {
		if args, err = AppendCapabilities(args, st.Caps); err != nil {
			return args, err
		}
	}
	if !st.Network {
		args = append(args, "--network", "none")
	}
	return append(args, image, buildProbeContainerCommand, "-c", "sleep 3600"), nil
}

// buildProbeStageCommands write the build context from inside the guest's own shell, because `podman cp` into a
// running container streams a tar the builder image does not ship and dies with a closed pipe. Without this the
// probe would be measuring a missing binary rather than a build.
func buildProbeStageCommands(dockerfile string) []string {
	lines := []string{}
	for _, line := range strings.Split(strings.TrimRight(dockerfile, "\n"), "\n") {
		lines = append(lines, ShellQuote(line))
	}
	writeDockerfile := "printf '%s\n' " + strings.Join(lines, " ") + " > " + buildProbeContextPath + "/Dockerfile"
	writePayload := "printf 'probe\n' > " + buildProbeContextPath + "/" + buildProbePayloadName
	return []string{
		"mkdir -p " + buildProbeContextPath,
		writeDockerfile,
		writePayload,
	}
}

// buildProbeBuildArgs is the build itself. The store roots are inside the container, so a stage cannot
// write the host's image store even in principle, and the tag is a probe-local name no product path
// resolves.
func buildProbeBuildArgs(container string) []string {
	return []string{"exec", container, "buildah",
		"--root", buildProbeStoreRoot,
		"--runroot", buildProbeRunRoot,
		"--storage-driver", buildProbeStorageDriver,
		"bud", "--isolation", "chroot",
		"-t", "mike-build-probe:latest",
		buildProbeContextPath,
	}
}

// buildProbeReason picks the line that names a build failure. It looks for the failed operation first
// ("operation not permitted" and its neighbours), because that is the whole answer for a capability stage
// and the whole answer for a store-on-the-wrong-filesystem stage; podman's own last-useful-line picker is
// right for a create or a start and wrong here, where buildah closes with "(Unable to determine exit
// status)" and opens with a step number.
func buildProbeReason(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// The operation that failed, not how far the build got. buildah opens with its progress
	// ("STEP 1/6: FROM …"), and a stage reported by a step number teaches nothing about the grant or the
	// storage it was measuring.
	for _, signal := range []string{"operation not permitted", "permission denied", "not connected", "no space left", "no such file"} {
		for _, line := range lines {
			if !strings.Contains(strings.ToLower(line), signal) {
				continue
			}
			if _, after, found := strings.Cut(line, `msg="`); found {
				return clip(strings.TrimSuffix(after, "\""), probeReasonMax)
			}
			return clip(strings.TrimSpace(line), probeReasonMax)
		}
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, after, found := strings.Cut(line, `msg="`); found {
			msg := strings.TrimSuffix(after, "\"")
			if strings.Contains(msg, "Unable to determine exit status") {
				continue
			}
			return clip(msg, probeReasonMax)
		}
		return clip(line, probeReasonMax)
	}
	return probeReason(out)
}

// buildProbeFailureReason names why a stage failed: a missing image is a prerequisite with a command
// attached, and anything else is buildah's own cause.
func buildProbeFailureReason(image string, out []byte, err error) string {
	if imgErr := ImageAvailabilityError(image, out); imgErr != nil {
		return fmt.Sprintf("%v (pull the builder image with: podman pull %s)", imgErr, image)
	}
	if err == nil {
		return "the stage reported no error and produced no image"
	}
	return buildProbeReason(out)
}

// ProbeBuildOffline reports whether this host can build an image inside a gVisor container at all. The
// error is nil when a stage built one; the report carries every stage's reason either way.
func (r *Runtime) ProbeBuildOffline(ctx context.Context, image string) (BuildProbeReport, error) {
	rep := BuildProbeReport{Image: image, Storage: buildProbeStorageDriver}
	if err := ValidateImageRef(image); err != nil {
		return rep, err
	}
	stages := buildProbeOfflineStages()
	// `podman image exists` answers by exit status. Asked before a container is created, so a host
	// without the builder is told to pull it instead of being told the runtime is broken.
	if _, err := r.PodmanExecContext(ctx, "image", "exists", image); err != nil {
		rep.Results = append(rep.Results, BuildProbeResult{
			Stage:  stages[0].Name,
			Reason: fmt.Sprintf("builder image %s is not in this host's store — pull it with: podman pull %s", image, image),
		})
		return rep, fmt.Errorf("builder image %s is not available locally", image)
	}

	for _, st := range stages {
		res := r.probeBuildStage(ctx, image, st)
		rep.Results = append(rep.Results, res)
		if res.OK {
			return rep, nil
		}
	}
	last := rep.Results[len(rep.Results)-1]
	return rep, fmt.Errorf("no offline stage built an image in a gVisor container: %s: %s", last.Stage, last.Reason)
}

// ProbeBuild runs the offline stages and then the stage that executes a RUN step inside the guest.
// The error is nil only when that last stage passed, because a build that can COPY but cannot RUN is
// not a nested runtime — it is the interpreted build the alternative already describes.
func (r *Runtime) ProbeBuild(ctx context.Context, image string) (BuildProbeReport, error) {
	rep, err := r.ProbeBuildOffline(ctx, image)
	if err != nil {
		return rep, err
	}
	for _, st := range append([]BuildProbeStage{buildProbeRunStage()}, buildProbeHardeningStages()...) {
		res := r.probeBuildStage(ctx, image, st)
		rep.Results = append(rep.Results, res)
		if !res.OK && !st.Informational {
			return rep, fmt.Errorf("stage %s failed: %s", res.Stage, res.Reason)
		}
	}
	return rep, nil
}

// buildProbeContainerName names one stage's container. It stays outside the `kanban-sandbox-` and
// `kanban-ext-` families, which the boot sweep and the e2e teardown delete by name prefix.
func buildProbeContainerName(stage string) string {
	return fmt.Sprintf("%s-%d-%s", buildProbeContainerPrefix, os.Getpid(), stage)
}

// probeBuildStage runs one stage end to end: create, start, stage the context in, build, remove. Each
// podman call is bounded by a context of its own derived from the caller's, so a wedged child is
// cancelled rather than left holding its output pipes.
func (r *Runtime) probeBuildStage(ctx context.Context, image string, st BuildProbeStage) BuildProbeResult {
	res := BuildProbeResult{Stage: st.Name, Caps: st.Caps, Network: st.Network, MeasuresRun: st.MeasuresRun, Informational: st.Informational}
	name := buildProbeContainerName(st.Name)

	createArgs, err := buildProbeCreateArgs(name, image, st)
	if err != nil {
		res.Reason = err.Error()
		return res
	}

	ctx, cancelStage := context.WithTimeout(ctx, BuildProbeStageTimeout)
	defer cancelStage()

	if out, err := r.PodmanExecContext(ctx, createArgs...); err != nil {
		res.Reason = buildProbeFailureReason(image, out, err)
		return res
	}
	// Removed whatever else happens: a stage that leaves a container behind teaches the next run
	// nothing and costs the host a stopped container each time it is asked.
	defer func() {
		if rmErr := r.RemoveContainer(name); rmErr != nil {
			logging.For(logging.Runtime).Warnf("build probe container %s survived the stage: %v", name, rmErr)
		}
	}()

	if out, err := r.PodmanExecContext(ctx, "--runtime-flag", "ignore-cgroups=true", "start", name); err != nil {
		res.Reason = buildProbeFailureReason(image, out, err)
		return res
	}

	for _, cmd := range buildProbeStageCommands(st.Dockerfile) {
		if out, err := r.PodmanExecContext(ctx, "exec", name, buildProbeContainerCommand, "-c", cmd); err != nil {
			res.Reason = "staging the build context in the guest: " + buildProbeReason(out)
			return res
		}
	}
	// The Dockerfile is the one file whose presence says the writes landed where the build will look.
	if out, err := r.PodmanExecContext(ctx, "exec", name, "test", "-f", buildProbeContextPath+"/Dockerfile"); err != nil {
		res.Reason = "staged context did not land at " + buildProbeContextPath + ": " + buildProbeReason(out)
		return res
	}

	out, err := r.PodmanExecContext(ctx, buildProbeBuildArgs(name)...)
	if err != nil {
		res.Reason = buildProbeFailureReason(image, out, err)
		logging.For(logging.Runtime).Warnf("build probe stage %s failed: %s", st.Name, res.Reason)
		return res
	}
	// A build that reports success but left nothing in the guest's store is the failure this stage
	// exists to catch: the commit is where a store on the wrong filesystem falls over.
	if out, err := r.PodmanExecContext(ctx, "exec", name, "buildah",
		"--root", buildProbeStoreRoot, "--runroot", buildProbeRunRoot,
		"--storage-driver", buildProbeStorageDriver, "images", "-q"); err != nil || strings.TrimSpace(string(out)) == "" {
		res.Reason = "the build reported success and left no image in the guest's store: " + buildProbeReason(out)
		return res
	}
	res.OK = true
	logging.For(logging.Runtime).Infof("build probe stage %s built an image under runsc (storage %s)", st.Name, buildProbeStorageDriver)
	return res
}

// stageBuildProbeContext is gone on purpose: see buildProbeStageCommands for why the context is
// written by the guest rather than copied from the host.
