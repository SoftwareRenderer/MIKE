package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/types"
)

// installAdmission swaps the budget for the duration of one test.
func installAdmission(t *testing.T, admit MemoryAdmission) {
	t.Helper()
	previous := buildMemAdmission
	t.Cleanup(func() { SetMemoryAdmission(previous) })
	SetMemoryAdmission(admit)
}

// A build belongs to no task, so nothing else books its memory: the amount is the limit its container is
// created with, and a project with no limit books nothing either way.
func TestBuildBooksTheLimitItsContainerGets(t *testing.T) {
	var got []int64
	installAdmission(t, func(_ context.Context, amountMB int64, _ func()) (func(), error) {
		got = append(got, amountMB)
		return func() {}, nil
	})

	for _, tc := range []struct {
		name  string
		limit int
		want  int64
	}{
		{"a configured limit is booked", 2048, 2048},
		{"an unlimited project books nothing", 0, 0},
	} {
		release, err := reserveBuildMemory(context.Background(), &types.Project{ID: "p", SandboxMemoryLimit: tc.limit}, func(string) {})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		release()
		if tc.want == 0 {
			if len(got) != 0 {
				t.Errorf("%s: the budget saw %v, want no call at all", tc.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: the budget saw %v, want exactly %d MB", tc.name, got, tc.want)
		}
		got = nil
	}
}

// The build waits *before* staging or starting anything, so a build that never gets room leaves no trace
// beyond its own refusal, and the stream says what it was waiting on while it waited.
func TestBuildWaitsForTheBudgetBeforeItStartsAnything(t *testing.T) {
	queued := make(chan struct{})
	stayFull := make(chan struct{})
	installAdmission(t, func(ctx context.Context, _ int64, waiting func()) (func(), error) {
		if waiting != nil {
			waiting()
		}
		close(queued)
		select {
		case <-stayFull:
			return nil, errors.New("the budget stayed full")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	sm, project, _ := buildE2E(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY a /a\n",
		"a":          "a",
	}, "")
	project.SandboxMemoryLimit = 1024

	var mu sync.Mutex
	var lines []string
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := sm.BuildProjectImage(context.Background(), project, BuildOptions{}, func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		})
		done <- outcome{err}
	}()

	select {
	case <-queued:
	case <-time.After(30 * time.Second):
		t.Fatal("the build never reached the budget")
	}
	// Still queued: nothing staged for the container, nothing started.
	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "waiting for the sandbox memory budget (1024 MB)") {
		t.Errorf("the stream never said what it was waiting for:\n%s", joined)
	}
	if strings.Contains(joined, "gVisor container") {
		t.Errorf("the build reached the container stage from the queue:\n%s", joined)
	}

	close(stayFull)
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "sandbox memory budget") {
			t.Errorf("error = %v, want it to name the budget it waited on", got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the build never came back from a budget that stayed full")
	}
}

// The reservation is the build's and it ends with the build: a plan-time refusal never books anything, and a
// build that runs hands its memory back.
func TestBuildReleasesItsReservationOnEveryExit(t *testing.T) {
	var releases atomic.Int32
	installAdmission(t, func(_ context.Context, _ int64, _ func()) (func(), error) {
		return func() { releases.Add(1) }, nil
	})

	sm, refused, _ := buildE2E(t, map[string]string{
		"Dockerfile": "RUN make\n",
	}, "")
	refused.SandboxMemoryLimit = 1024
	if _, err := sm.BuildProjectImage(context.Background(), refused, BuildOptions{}, nil); err == nil {
		t.Fatal("the build succeeded where the gate refuses it")
	}
	if releases.Load() != 0 {
		t.Fatalf("a build refused by the gate booked memory (%d releases)", releases.Load())
	}

	built, err := sm.BuildProjectImage(context.Background(), buildableProject(t, sm), BuildOptions{}, nil)
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = exec.Command("podman", "rmi", built.Image).CombinedOutput()
	})
	if releases.Load() != 1 {
		t.Errorf("the budget saw %d releases, want the one reservation returned", releases.Load())
	}
}

// buildableProject is a second project in a repository of its own, so the tests above do not share an image name.
func buildableProject(t *testing.T, sm *SandboxManager) *types.Project {
	t.Helper()
	requireBuilder(t)
	repo := testRepo(t, map[string]string{"Dockerfile": "FROM scratch\nCOPY a /a\n", "a": "a\n"})
	return &types.Project{
		ID: "admissionok", Name: "admissionok", Path: repo, SandboxEnabled: true,
		SandboxDockerfilePath: filepath.Join(repo, "Dockerfile"), SandboxMemoryLimit: 1024,
	}
}
