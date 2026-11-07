package sandbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"mike/backend/types"
)

// buildCeiling bounds a build started by a turn or a terminal, which has no request context to cancel it.
const buildCeiling = 45 * time.Minute

// Where a project's sandbox image comes from. Decided in one place so the executor, the
// terminal paths and the file-system handlers cannot disagree about whether it exists.
const (
	// ImageSourceDockerfile: the image is built from the project's Dockerfile.
	ImageSourceDockerfile = "dockerfile"
	// ImageSourceConfigured: a registry image the project configured.
	ImageSourceConfigured = "configured"
	// ImageSourceDefault: the app-wide base image.
	ImageSourceDefault = "default"
)

// ProjectImage is a project's sandbox image and what has to happen to make it exist.
type ProjectImage struct {
	// Image is qualified before podman sees it.
	Image string
	// Source is one of the ImageSource* values.
	Source string
	// Dockerfile is set only when Source == ImageSourceDockerfile.
	Dockerfile string
}

// NeedsBuild reports whether the image is produced on this host rather than pulled.
func (pi ProjectImage) NeedsBuild() bool { return pi.Source == ImageSourceDockerfile }

// ResolveProjectImage decides which image a project's sandbox runs on. Pure — no podman, no
// filesystem — so the rule every caller shares is easy to test.
func ResolveProjectImage(project *types.Project) ProjectImage {
	if project == nil {
		return ProjectImage{Image: BaseImage, Source: ImageSourceDefault}
	}
	if project.SandboxDockerfilePath != "" {
		return ProjectImage{
			Image:      ProjectImageName(project.Name),
			Source:     ImageSourceDockerfile,
			Dockerfile: project.SandboxDockerfilePath,
		}
	}
	if project.SandboxImage != "" {
		return ProjectImage{Image: project.SandboxImage, Source: ImageSourceConfigured}
	}
	return ProjectImage{Image: BaseImage, Source: ImageSourceDefault}
}

// imageOp is one in-flight image build or pull. Everyone watching it hears its progress and takes its answer.
type imageOp struct {
	wg sync.WaitGroup
	// err and result are the preparation's answer: written before wg.Done, read only after wg.Wait.
	err    error
	result *BuildResult

	// waitersMu guards waiters and announced. A listener runs with no lock held, so it may write a socket or
	// broadcast while the build keeps emitting.
	waitersMu sync.Mutex
	waiters   []func(types.SandboxImageStatus)
	// announced is the phase this preparation said it was doing, replayed to whoever joins later.
	announced *types.SandboxImageStatus
}

// announce says what this preparation is doing and remembers it, so a caller that joins later still learns
// what it is waiting on. The builder's next line can be a minute away.
func (op *imageOp) announce(status types.SandboxImageStatus) {
	op.waitersMu.Lock()
	op.announced = &status
	op.waitersMu.Unlock()
	op.progress(status)
}

// watch adds one caller's progress listener, then hands it the phase already announced, so a caller that
// joins a running build learns what it is waiting on and not only what comes next.
func (op *imageOp) watch(report func(types.SandboxImageStatus)) {
	if report == nil {
		return
	}
	op.waitersMu.Lock()
	op.waiters = append(op.waiters, report)
	announced := op.announced
	op.waitersMu.Unlock()
	if announced != nil {
		report(*announced)
	}
}

// progress hands one event to everyone watching, including the caller doing the work, from the emitting
// goroutine: a listener that writes a socket is writing it there.
func (op *imageOp) progress(status types.SandboxImageStatus) {
	op.waitersMu.Lock()
	listeners := make([]func(types.SandboxImageStatus), len(op.waiters))
	copy(listeners, op.waiters)
	op.waitersMu.Unlock()
	for _, fn := range listeners {
		fn(status)
	}
}

// await is what the preparation this caller joined produced.
func (op *imageOp) await() (*BuildResult, error) {
	op.wg.Wait()
	return op.result, op.err
}

// joinImageOp finds the preparation of image already running and starts sending this caller its progress; it
// reports false when nothing is in flight. imageOpsMu is never held across the wait, because the finisher
// takes it to retire the key.
func (sm *SandboxManager) joinImageOp(image string, report func(types.SandboxImageStatus)) (*imageOp, bool) {
	sm.imageOpsMu.Lock()
	op := sm.imageOps[image]
	if op == nil {
		sm.imageOpsMu.Unlock()
		return nil, false
	}
	op.watch(report)
	sm.imageOpsMu.Unlock()
	return op, true
}

// claimImageOp records this caller as the preparation of image in flight, with report as the first listener.
// It reports false when another caller holds the key.
func (sm *SandboxManager) claimImageOp(image string, report func(types.SandboxImageStatus)) (*imageOp, bool) {
	sm.imageOpsMu.Lock()
	defer sm.imageOpsMu.Unlock()
	if _, busy := sm.imageOps[image]; busy {
		return nil, false
	}
	op := &imageOp{}
	op.wg.Add(1)
	op.watch(report)
	if sm.imageOps == nil {
		sm.imageOps = make(map[string]*imageOp)
	}
	sm.imageOps[image] = op
	return op, true
}

// finishImageOp retires the in-flight key and releases everyone waiting with the answer they will share.
func (sm *SandboxManager) finishImageOp(image string, op *imageOp, result *BuildResult, err error) {
	sm.imageOpsMu.Lock()
	if sm.imageOps[image] == op {
		delete(sm.imageOps, image)
	}
	sm.imageOpsMu.Unlock()
	op.result, op.err = result, err
	op.wg.Done()
}

// runImageOp runs the work the claimed key stands for and publishes its answer, on the panic path too. A key
// left claimed would block every later caller for the life of the process.
func (sm *SandboxManager) runImageOp(image string, op *imageOp, run func() (*BuildResult, error)) (result *BuildResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			sm.finishImageOp(image, op, nil, fmt.Errorf("sandbox image preparation panicked: %v", r))
			panic(r)
		}
	}()
	result, err = run()
	sm.finishImageOp(image, op, result, err)
	return result, err
}

// EnsureImageForProject makes sure the project's sandbox image exists, by build or by pull, reporting progress
// through onProgress (nil is fine). It takes no context: closing a dialog must not cancel a build.
//
// A preparation already running for that image is waited for and its progress reported, so a task or a
// terminal started mid-build reads that build rather than racing past it.
func (sm *SandboxManager) EnsureImageForProject(project *types.Project, onProgress func(status types.SandboxImageStatus)) (string, error) {
	pi := ResolveProjectImage(project)

	// An unreadable Dockerfile is a configuration mistake; say so before waiting behind a build.
	if pi.NeedsBuild() {
		if _, err := os.Stat(pi.Dockerfile); err != nil {
			return pi.Image, fmt.Errorf("project Dockerfile %s is not readable: %w", pi.Dockerfile, err)
		}
	}

	// Qualified for every source, because a stored ref may still be a short name.
	image := sm.rt.ResolveImageRef(pi.Image)
	phase := types.SandboxImagePhasePulling
	if pi.NeedsBuild() {
		phase = types.SandboxImagePhaseBuilding
	}
	report := func(status types.SandboxImageStatus) {
		if onProgress != nil {
			status.Image = image
			onProgress(status)
		}
	}
	finish := func(err error) (string, error) {
		if err != nil {
			report(types.SandboxImageStatus{Phase: types.SandboxImagePhaseFailed, Error: err.Error()})
			return image, err
		}
		report(types.SandboxImageStatus{Phase: types.SandboxImagePhaseDone})
		return image, nil
	}

	// Waiting is decided before asking the host whether the image exists, because a build in flight leaves the
	// bytes it is replacing in place. Nothing is cached, so a later caller tries again.
	for {
		if op, joined := sm.joinImageOp(image, report); joined {
			_, err := op.await()
			return finish(err)
		}
		if sm.ImageReady(image) {
			return image, nil
		}
		op, claimed := sm.claimImageOp(image, report)
		if !claimed {
			// Claimed a moment ago by someone else; wait for them the way anyone who joined does.
			continue
		}
		_, err := sm.runImageOp(image, op, func() (*BuildResult, error) {
			return sm.makeProjectImage(op, image, pi, project, phase)
		})
		return finish(err)
	}
}

// makeProjectImage is the work published under an image's in-flight key: build it from the project's
// Dockerfile, or pull the image the project configured. Only the holder of the key calls it, so it uses the
// builder that does not consult the table — one that did would wait on itself.
func (sm *SandboxManager) makeProjectImage(op *imageOp, image string, pi ProjectImage, project *types.Project, phase string) (*BuildResult, error) {
	// The caller that won the race has done the work for all of them. The phase is announced after that check,
	// so a preparation needing no work never claims it is doing some.
	if sm.ImageReady(image) {
		// A project image built here carries its cache key, so a matching image is reused without staging
		// anything; one that is older, or built from other input, is rebuilt.
		if !pi.NeedsBuild() {
			return nil, nil
		}
		if key, keyErr := sm.projectBuildKey(context.Background(), project); keyErr == nil &&
			sm.ImageCarriesBuild(image, key) {
			return nil, nil
		}
	}
	op.announce(types.SandboxImageStatus{Phase: phase})
	report := func(line string) { op.progress(types.SandboxImageStatus{Phase: phase, Line: line}) }
	if pi.NeedsBuild() {
		ctx, cancel := context.WithTimeout(context.Background(), buildCeiling)
		defer cancel()
		result, err := sm.buildProjectImageDirect(ctx, project, report)
		if err != nil {
			return nil, fmt.Errorf("build sandbox image %s: %w", image, err)
		}
		return result, nil
	}
	if err := sm.PullImageWithProgress(image, report); err != nil {
		return nil, fmt.Errorf("pull sandbox image %s: %w", image, err)
	}
	return nil, nil
}
