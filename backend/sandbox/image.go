package sandbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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

// imageKey is the key of the in-flight table for one project image. It is the image reference the host
// actually uses, because a reference stored on a project may still be a short name. Every caller that starts,
// joins, watches or stops a preparation works out the key this way, or two of them end up naming different
// builds.
func (sm *SandboxManager) imageKey(pi ProjectImage) string {
	return sm.rt.ResolveImageRef(pi.Image)
}

// imageOp is one in-flight image build or pull. Everyone watching it hears its progress and takes its answer.
type imageOp struct {
	// done is closed when the preparation answers. err and result are written before that close and read only
	// after it. It is a channel rather than a WaitGroup because a waiter may stop listening without taking the
	// answer, and the answer still has to be there for the others.
	done chan struct{}
	err  error

	result *BuildResult

	// waitersMu guards waiters, announced and cancel. A listener runs with no lock held, so it may write a
	// socket or broadcast while the build keeps emitting.
	waitersMu sync.Mutex
	waiters   []func(types.SandboxImageStatus)
	// announced is the phase this preparation said it was doing, replayed to whoever joins later.
	announced *types.SandboxImageStatus
	// cancel stops the work this key stands for. The build is a subprocess and only a cancelled context ends
	// one, so this is the only way out of it. It is nil between claiming the key and starting the work, which
	// is the only moment in which a cancel has nothing to stop.
	cancel context.CancelFunc
	// stopped says a person asked for this preparation to end. Its error cannot say that, because a cancelled
	// context leaves podman's exit status behind rather than a context error, and without this flag the failure
	// of a build somebody stopped reads as a fault.
	stopped atomic.Bool
}

// setCancel records the function that stops the work this key stands for.
func (op *imageOp) setCancel(cancel context.CancelFunc) {
	op.waitersMu.Lock()
	op.cancel = cancel
	op.waitersMu.Unlock()
}

// stopWork asks the preparation to end, and reports whether there was a context to cancel.
func (op *imageOp) stopWork() bool {
	op.waitersMu.Lock()
	cancel := op.cancel
	op.waitersMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	op.stopped.Store(true)
	return true
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
	<-op.done
	return op.result, op.err
}

// answered waits for the preparation to end and reports how it ended. It returns false when ctx ends first;
// leaving early says nothing about the preparation, which goes on running for everyone else. What the
// preparation produced is `await`'s question.
func (op *imageOp) answered(ctx context.Context) (error, bool) {
	select {
	case <-op.done:
		return op.err, true
	case <-ctx.Done():
		return nil, false
	}
}

// imageWatch is one viewer's queue. It holds the statuses a preparation sends, so that how fast the viewer
// reads never decides how fast the build runs.
//
// A full queue drops instead of blocking. The producer is the goroutine printing the build's own lines, so a
// viewer that stopped reading must not be able to stall a build.
type imageWatch struct {
	statuses chan types.SandboxImageStatus
	// The preparation keeps sending to every listener it was handed, even after the viewer has gone, so the
	// flag and the close are guarded: a send on a closed channel panics in the sender.
	mu     sync.Mutex
	closed bool
}

// push hands one status to the watcher, dropping it when the watcher cannot take it.
func (w *imageWatch) push(status types.SandboxImageStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	select {
	case w.statuses <- status:
	default:
	}
}

// finish ends the watcher's queue, once. The preparation answering and the viewer leaving both arrive here,
// and either one means no more reading. The lock covers the flag and the close and nothing else, so a build
// that is still sending never waits on a viewer.
func (w *imageWatch) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.statuses)
	}
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
	op := &imageOp{done: make(chan struct{})}
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
	close(op.done)
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

	image := sm.imageKey(pi)
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
	// The preparation is bounded by its own time limit and by the Cancel button, and not by the request that
	// needed the image. A task turn or an opening terminal waits behind the build but does not own it.
	ctx, cancel := context.WithTimeout(context.Background(), buildCeiling)
	defer cancel()
	op.setCancel(cancel)
	if pi.NeedsBuild() {
		result, err := sm.buildProjectImageDirect(ctx, project, report)
		if err != nil {
			return nil, fmt.Errorf("build sandbox image %s: %w", image, err)
		}
		return result, nil
	}
	if err := sm.PullImageWithProgress(ctx, image, report); err != nil {
		return nil, fmt.Errorf("pull sandbox image %s: %w", image, err)
	}
	return nil, nil
}

// watchQueueLines is how far behind one viewer may fall before its own lines start being dropped. A viewer
// this far behind has stopped reading, and the build must not wait for one.
const watchQueueLines = 256

// WatchImagePreparation attaches one viewer to the build or pull of this project's image that is already
// running. It never starts work, and the second result is false when nothing is running.
//
// The channel carries the phase the preparation announced before this caller arrived, every line after that,
// and finally how it ended. What it printed before the call is not replayed. The channel closes when the
// preparation answers or when ctx ends. A ctx ending says nothing about the preparation, which goes on running
// for everyone else.
func (sm *SandboxManager) WatchImagePreparation(ctx context.Context, project *types.Project) (<-chan types.SandboxImageStatus, bool) {
	if project == nil {
		return nil, false
	}
	image := sm.imageKey(ResolveProjectImage(project))
	watch := &imageWatch{statuses: make(chan types.SandboxImageStatus, watchQueueLines)}
	op, joined := sm.joinImageOp(image, watch.push)
	if !joined {
		return nil, false
	}
	go func() {
		defer watch.finish()
		err, answered := op.answered(ctx)
		if !answered {
			return
		}
		// The ending frame goes through the same queue as the lines, so a viewer that is dropping lines loses
		// this one too. Its reader still sees the channel close.
		switch {
		case err == nil:
			watch.push(types.SandboxImageStatus{Phase: types.SandboxImagePhaseDone, Image: image})
		case op.stopped.Load():
			watch.push(types.SandboxImageStatus{Phase: types.SandboxImagePhaseCancelled, Image: image})
		default:
			watch.push(types.SandboxImageStatus{Phase: types.SandboxImagePhaseFailed, Image: image, Error: err.Error()})
		}
	}()
	return watch.statuses, true
}

// CancelImagePreparation stops the build or pull of this project's image that is in flight, which kills its
// container. The caller that started it takes the failure the way it takes any other failed preparation, and
// nothing starts the build again without being asked. That caller may be this dialog, a task waiting for its
// image, or a terminal being opened.
//
// It returns false when nothing is running. A Cancel pressed a moment after the build ended asked for
// something that is already true.
func (sm *SandboxManager) CancelImagePreparation(project *types.Project) bool {
	if project == nil {
		return false
	}
	image := sm.imageKey(ResolveProjectImage(project))
	sm.imageOpsMu.Lock()
	op := sm.imageOps[image]
	sm.imageOpsMu.Unlock()
	return op != nil && op.stopWork()
}
