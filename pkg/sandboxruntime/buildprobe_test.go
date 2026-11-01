package sandboxruntime

import (
	"strings"
	"testing"
)

// The pure half of the build probe: what the ladder asks podman for. These hold without podman, which
// is the point — the argv is the claim, and a real container only confirms it.

func TestBuildProbeStageAsksForTheProductPrefaceAndNothingElse(t *testing.T) {
	st := BuildProbeStage{Name: "offline-sys-chroot", Caps: []string{"SYS_CHROOT"}}
	args, err := buildProbeCreateArgs("mike-build-probe-1-offline", "quay.io/buildah/stable:latest", st)
	if err != nil {
		t.Fatalf("buildProbeCreateArgs: %v", err)
	}
	joined := strings.Join(args, " ")

	// The same posture every product container starts from, or the measurement is of something else.
	for _, want := range []string{"--runtime runsc", "--cap-drop ALL", "--security-opt no-new-privileges", "--pull never", "--cap-add SYS_CHROOT", "--network none"} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args lost %q: %s", want, joined)
		}
	}
	// And nothing the product does not grant a build: one mount of the host tree, one published port,
	// one inherited variable would each measure a container MIKE cannot make.
	for _, forbidden := range []string{"-v", "-p", "-e", "--device", "--privileged", "--network host"} {
		for _, a := range args {
			if a == forbidden {
				t.Errorf("create args carry %q, which no build container of this product has: %s", forbidden, joined)
			}
		}
	}
	// The image and its command are last, so no flag can swallow either.
	if args[len(args)-4] != "quay.io/buildah/stable:latest" {
		t.Errorf("image is not where the command begins: %s", joined)
	}
}

func TestBuildProbeCapabilityGrantRefusesACapabilityThatIsNotAName(t *testing.T) {
	// The one path to a grant checks the shape, so a stage cannot inject arguments through its own
	// capability list: the refusal comes with the argument list unchanged.
	st := BuildProbeStage{Name: "offline", Caps: []string{"SYS_ADMIN ALL"}}
	args, err := buildProbeCreateArgs("mike-build-probe-1", "img:1", st)
	if err == nil {
		t.Fatalf("a capability list carrying a space was accepted: %v", args)
	}
	for _, a := range args {
		if a == "--cap-add" {
			t.Errorf("the refusal still appended a grant: %v", args)
		}
	}
}

func TestBuildProbeOnlyTheRunStageNeedsNetwork(t *testing.T) {
	for _, st := range buildProbeOfflineStages() {
		if st.Network || st.MeasuresRun {
			t.Errorf("offline stage %s asks for network or a RUN step: %+v", st.Name, st)
		}
	}
	run := buildProbeRunStage()
	if !run.Network || !run.MeasuresRun {
		t.Errorf("the run stage does not measure what its name says: %+v", run)
	}
	// A base image that names no registry would be resolved differently inside the guest than the
	// product resolves every ref, which is a bug the stage would hide as a build failure.
	if !strings.Contains(run.Dockerfile, "docker.io/") {
		t.Errorf("the run stage's FROM names no registry: %s", run.Dockerfile)
	}
}

func TestBuildProbeStoreRootsAreInsideTheContainer(t *testing.T) {
	args := buildProbeBuildArgs("mike-build-probe-1-offline")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--storage-driver "+buildProbeStorageDriver) {
		t.Errorf("the storage driver is not the one that needs no device: %s", joined)
	}
	if !strings.Contains(joined, "--isolation chroot") {
		t.Errorf("the stage is not measuring a chroot build: %s", joined)
	}
	// The probe declares one in-guest root. Anything absolute outside it is a host path reaching a
	// command that has no business naming one, and a push or a commit to the host store would put the
	// result where a later task could boot it.
	for i, a := range args {
		if strings.HasPrefix(a, "/") && !strings.HasPrefix(a, buildProbeStoreDir) {
			t.Errorf("arg %d is absolute and outside the declared in-guest root %s: %s", i, buildProbeStoreDir, a)
		}
	}
	for _, forbidden := range []string{"push", "commit", "--privileged"} {
		for _, a := range args {
			if a == forbidden {
				t.Errorf("the stage runs %q, which writes outside the container: %s", forbidden, joined)
			}
		}
	}
}

func TestStaleBuildProbeFilterSweepsOnlyTheProbeFamily(t *testing.T) {
	// podman's `--filter name=` takes a regular expression matched against the whole name, so "contains"
	// is the semantics to assert. The point of the assertion is the second list: the sweep a probe owns
	// must never be able to name a live sandbox.
	for _, name := range []string{"mike-build-probe-42-offline", "mike-build-probe-42-run-step"} {
		if !strings.Contains(name, StaleBuildProbeFilter) {
			t.Errorf("%s is a probe stage container the sweep would skip", name)
		}
	}
	for _, name := range []string{
		"kanban-sandbox-frost-amber", "kanban-sandbox-proj-MIKE", "kanban-ext-lsp-gopls",
		"mike-sandbox-probe-99", "buildkitd",
	} {
		if strings.Contains(name, StaleBuildProbeFilter) {
			t.Errorf("%s would be deleted by the probe's own sweep", name)
		}
	}
}

func TestBuildProbeContainerNameStaysOutsideTheSweptFamilies(t *testing.T) {
	name := buildProbeContainerName("offline")
	for _, prefix := range []string{"kanban-sandbox-", "kanban-ext-"} {
		if strings.HasPrefix(name, prefix) {
			t.Errorf("%s would be deleted by the boot sweep or the e2e teardown", name)
		}
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			t.Fatalf("container name %q holds %q, outside podman's name charset", name, c)
		}
	}
}

func TestBuildGrantIsTheMeasuredSet(t *testing.T) {
	// The grant is a literal list in this package, so its shape is checked here rather than at a call
	// site: one entry with a space or a dash would arrive at podman as further arguments.
	if err := ValidateCapabilities(BuildGrant); err != nil {
		t.Fatalf("the probe's own grant is not a list of capability names: %v", err)
	}
	// These three are what was measured, each one necessary (docs/adr/0002). Capabilities are
	// non-monotone here — a superset of this set fails where the set passes — so an edit here is a new
	// candidate grant and re-owes `make build-probe`; a larger list is not automatically safer.
	if got, want := strings.Join(BuildGrant, ","), "SETUID,SETGID,SETFCAP"; got != want {
		t.Errorf("the grant is %s; the measured set is %s, and a change to it is a new candidate", got, want)
	}
}

func TestBuildProbeStagingQuotesEveryLineOfTheDockerfile(t *testing.T) {
	st := BuildProbeStage{Dockerfile: "FROM scratch\nRUN echo 'hi' > /x\n"}
	cmds := buildProbeStageCommands(st.Dockerfile)
	if len(cmds) != 3 {
		t.Fatalf("staging is %d commands, want mkdir, Dockerfile, payload: %v", len(cmds), cmds)
	}
	joined := strings.Join(cmds, " && ")
	for _, want := range []string{"'FROM scratch'", "'RUN echo '\\''hi'\\'' > /x'", buildProbeContextPath + "/Dockerfile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("staging lost %q in: %s", want, joined)
		}
	}
	// A line written unquoted is a command the guest would run while staging, not text in a file.
	if strings.Contains(cmds[1], "; rm") || strings.Contains(cmds[1], "$(") {
		t.Errorf("a Dockerfile line reached the shell as syntax: %s", cmds[1])
	}
}

func TestBuildProbeFailureReasonNamesThePrerequisite(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		err   error
		wants string
	}{
		{
			name:  "missing image",
			out:   "Error: creating container: image not known",
			err:   errStub{},
			wants: "podman pull quay.io/buildah/stable:latest",
		},
		{
			name: "buildah's cause, not its wrapper",
			out: `time="2026-10-30T20:16:02Z" level=error msg="writing /proc/15/gid_map: operation not permitted"` +
				"\n" + `time="2026-10-30T20:16:02Z" level=error msg="(Unable to determine exit status)"`,
			err:   errStub{},
			wants: "gid_map",
		},
		{name: "a bare line", out: "mount out of range", err: errStub{}, wants: "mount out of range"},
		{name: "no error and no image", out: "", err: nil, wants: "produced no image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProbeFailureReason("quay.io/buildah/stable:latest", []byte(tt.out), tt.err)
			if !strings.Contains(got, tt.wants) {
				t.Errorf("reason %q does not say %q", got, tt.wants)
			}
		})
	}
}

type errStub struct{}

func (errStub) Error() string { return "stub" }

// The half that starts a container lives in backend/sandbox, where sandboxcheck can be imported
// without this package importing its own test util.

// A build grants this set and nothing else, so it has to be both the set the nested builder hands a `RUN`
// step and a superset of the measured minimum: a name missing from it is a build that dies on a permission
// check, and a set assembled by dropping one name from it is a measured failure (docs/adr/0002).
func TestBuilderGrantIsTheSetTheNestedBuilderAsksFor(t *testing.T) {
	// A literal list in this package, so its shape is checked here: one entry with a space or a dash would
	// reach podman as further arguments.
	if err := ValidateCapabilities(BuilderGrant); err != nil {
		t.Fatalf("the build grant is not a list of capability names: %v", err)
	}
	union := map[string]bool{}
	for _, cap := range BuilderGrant {
		if union[cap] {
			t.Errorf("%s is listed twice in the build grant", cap)
		}
		union[cap] = true
	}
	for _, cap := range BuildGrant {
		if !union[cap] {
			t.Errorf("the build grant lost %s, which every build needs", cap)
		}
	}
	// The names the Sandbox Configuration's "defaults" button offers, without the CAP_ prefix this
	// package takes. The set a person can grant a task container with one click is the set a build gets.
	for _, cap := range []string{
		"CHOWN", "DAC_OVERRIDE", "FSETID", "FOWNER", "MKNOD", "NET_RAW", "SETGID", "SETUID",
		"SETFCAP", "SETPCAP", "NET_BIND_SERVICE", "SYS_CHROOT", "KILL", "AUDIT_WRITE",
	} {
		if !union[cap] {
			t.Errorf("the build grant is missing %s from the recommended set", cap)
		}
	}
	// The failing set recorded in docs/adr/0002 is this one less CHOWN, so the grant that ships is not it.
	if _, ok := union["CHOWN"]; !ok {
		t.Error("the build grant dropped CHOWN, which makes it the already-measured failing set")
	}
	// SYS_ADMIN is what a person reaches for, and it is not part of what a builder asks for: a build
	// needing it is a build this venue declines, not one this list grows to cover.
	if union["SYS_ADMIN"] {
		t.Error("the build grant carries SYS_ADMIN, which no build step was measured needing")
	}
}

// The harder Dockerfile is the answer to "a grant measured against two one-line stages". If it stops asking
// for the work that broke a larger grant, it measures nothing new.
func TestBuildProbeHardeningStagesExerciseTheOwnershipPaths(t *testing.T) {
	stages := buildProbeHardeningStages()
	// the shipped grant, the measured minimum, the shipped grant minus CHOWN, the wide superset, and that
	// superset minus CHOWN. The pairs are the finding: one name is the difference in each.
	if len(stages) != 5 {
		t.Fatalf("the ladder has %d hardening stages, want the required build and the four grants measured for information", len(stages))
	}
	wide := map[string][]string{}
	for _, st := range stages {
		wide[st.Name] = st.Caps
	}
	if len(wide["superset-no-chown"]) != len(wide["superset-15"])-1 {
		t.Error("the recorded failing set is not the wide superset minus one capability")
	}
	if got, want := len(wide["grant-without-chown"]), len(BuilderGrant)-1; got != want {
		t.Errorf("the failing set holds %d capabilities, want the shipped grant minus CHOWN (%d)", got, want)
	}
	for _, cap := range wide["grant-without-chown"] {
		if cap == "CHOWN" {
			t.Error("the recorded failing set grants CHOWN, so it measures nothing")
		}
	}
	// One row per stage is the cheap half of "the probe measures the product": the required rung runs the
	// grant a build gets, and the minimum is still measured, for information, where it was first measured.
	if got := wide["heavy-run"]; !sameCapSet(got, BuilderGrant) {
		t.Errorf("the required rung measures %v, want the shipped grant %v", got, BuilderGrant)
	}
	if got := wide["minimum-heavy"]; !sameCapSet(got, BuildGrant) {
		t.Errorf("the minimum stage measures %v, want %v", got, BuildGrant)
	}
	heavy := stages[0]
	if heavy.Informational {
		t.Error("the heavier build is a required measurement, not an informational one")
	}
	for _, st := range stages[1:] {
		if !st.Informational {
			t.Errorf("stage %s must not fail the ladder: what a larger grant does is a fact to report", st.Name)
		}
	}
	for _, want := range []string{"chown -R", "chmod", "ln -s", "ln /d", "touch -d", "COPY hello", "USER ", "WORKDIR "} {
		if !strings.Contains(heavy.Dockerfile, want) {
			t.Errorf("the heavier build never does %q, so it measures the same two-line ladder:\n%s", want, heavy.Dockerfile)
		}
	}
	if !strings.Contains(heavy.Dockerfile, "docker.io/") {
		t.Errorf("the heavier build's FROM names no registry: %s", heavy.Dockerfile)
	}
	for _, st := range stages {
		if !st.MeasuresRun {
			t.Errorf("stage %s does not run a step", st.Name)
		}
	}
}

// An informational stage is neither a pass nor the run's failure.
func TestBuildProbeInformationalStageCannotCarryTheLadder(t *testing.T) {
	rep := BuildProbeReport{Results: []BuildProbeResult{{Stage: "union-grant", OK: true, Informational: true, MeasuresRun: true}}}
	if rep.Passed() || rep.RunsWithinGuest() {
		t.Error("an informational stage reported the ladder as passed")
	}
	rep.Results = append(rep.Results, BuildProbeResult{Stage: "run-step", OK: true, MeasuresRun: true})
	if !rep.Passed() || !rep.RunsWithinGuest() {
		t.Error("the required stage did not carry the ladder beside an informational one")
	}
}

// A capability stage and a storage stage are the two things this ladder can fail for, and both name
// themselves in one line. Reported as "STEP 1/6: FROM …", the answer is useless to whoever is asked to fix
// it — which is what the first measurement of the failing grant printed.
func TestBuildProbeReasonNamesTheFailedOperation(t *testing.T) {
	captured := "STEP 1/6: FROM docker.io/library/alpine:3.20\n" +
		"Trying to pull docker.io/library/alpine:3.20...\n" +
		"Copying blob sha256:25f1d6b1951ac8eb3740558fe94cb83d377bdadf95fd9f98b50d2e1b96130471\n" +
		"time=\"2026-10-30T22:42:15Z\" level=error msg=\"While applying layer: ApplyLayer stdout:  stderr: lchown /etc/shadow: operation not permitted exit status 1\"\n" +
		"Error: creating build container: unable to copy from source docker://alpine:3.20\n"
	got := buildProbeReason([]byte(captured))
	for _, want := range []string{"lchown /etc/shadow", "operation not permitted"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reason does not name %q: %q", want, got)
		}
	}
	if strings.Contains(got, "STEP 1/6") {
		t.Errorf("the reason is buildah's progress, not its cause: %q", got)
	}

	// A failure with no failed-operation line still reports something: the first thing the child said.
	first := buildProbeReason([]byte("STEP 2/6: RUN /bin/true\n(Unable to determine exit status)\n"))
	if first == "" || strings.Contains(first, "Unable to determine exit status") {
		t.Errorf("a stage with no cause line reported %q", first)
	}
}

// sameCapSet compares two grants as sets: the order capabilities reach podman in decides nothing, and a grant
// compared as a list reports a difference that is not one.
func sameCapSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, cap := range got {
		seen[cap]++
	}
	for _, cap := range want {
		seen[cap]--
		if seen[cap] < 0 {
			return false
		}
	}
	return true
}
