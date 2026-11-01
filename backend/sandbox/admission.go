package sandbox

import (
	"context"
	"fmt"

	"mike/backend/types"
)

// MemoryAdmission books memory against the sandbox budget until the returned function releases it, calling
// `waiting` if the call had to queue. It is a function, not a gate type, because this package must not import the
// executor.
type MemoryAdmission func(ctx context.Context, amountMB int64, waiting func()) (release func(), err error)

// buildMemAdmission is the installed budget, or nil when this process has none. A build with no budget runs
// unbooked, which is also what happens on an install configured with a 0 limit (the gate treats 0 as disabled).
var buildMemAdmission MemoryAdmission

// SetMemoryAdmission wires the sandbox memory budget into image builds. A build container belongs to no task, so
// the supervisor that charges running containers never sees one.
func SetMemoryAdmission(admit MemoryAdmission) { buildMemAdmission = admit }

// reserveBuildMemory books what the build container is about to be given, for the build's whole duration.
func reserveBuildMemory(ctx context.Context, project *types.Project, emit func(string)) (func(), error) {
	return reserveMemory(ctx, buildMemoryReserveMB(project), emit)
}

// buildMemoryReserveMB is what a build books: its container's own limit, and nothing when the project left that
// blank, exactly as the task path does.
func buildMemoryReserveMB(project *types.Project) int {
	if project == nil || project.SandboxMemoryLimit <= 0 {
		return 0
	}
	return project.SandboxMemoryLimit
}

// reserveMemory is the seam the build path calls, so the no-budget case has one shape everywhere.
func reserveMemory(ctx context.Context, amountMB int, emit func(string)) (func(), error) {
	if buildMemAdmission == nil || amountMB <= 0 {
		return func() {}, nil
	}
	release, err := buildMemAdmission(ctx, int64(amountMB), func() {
		emit(fmt.Sprintf("waiting for the sandbox memory budget (%d MB)…", amountMB))
	})
	if err != nil {
		return nil, fmt.Errorf("the sandbox memory budget never had room for this build: %w", err)
	}
	if release == nil {
		return func() {}, nil
	}
	return release, nil
}
