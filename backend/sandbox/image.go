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

// imageOp is one in-flight image build or pull that later callers can wait on.
type imageOp struct {
	wg  sync.WaitGroup
	err error
}

// EnsureImageForProject makes sure the project's sandbox image exists, by build or by pull, reporting progress
// through onProgress (nil is fine). It takes no context: closing a dialog must not cancel a build.
func (sm *SandboxManager) EnsureImageForProject(project *types.Project, onProgress func(status types.SandboxImageStatus)) (string, error) {
	pi := ResolveProjectImage(project)

	// An unreadable Dockerfile is a configuration mistake; say so before waiting behind a build.
	if pi.NeedsBuild() {
		if _, err := os.Stat(pi.Dockerfile); err != nil {
			return pi.Image, fmt.Errorf("project Dockerfile %s is not readable: %w", pi.Dockerfile, err)
		}
	}

	// 	// Qualified here for every source, so a value stored before the rule existed still resolves.
	image := sm.rt.ResolveImageRef(pi.Image)
	if sm.ImageReady(image) {
		return image, nil
	}

	emit := func(status types.SandboxImageStatus) {
		if onProgress != nil {
			status.Image = image
			onProgress(status)
		}
	}
	phase := types.SandboxImagePhasePulling
	if pi.NeedsBuild() {
		phase = types.SandboxImagePhaseBuilding
	}
	emit(types.SandboxImageStatus{Phase: phase})

	err := sm.doOnce(image, func() error {
		// The caller that won the race has done the work for all of them.
		if sm.ImageReady(image) {
			// 			// A project image built here carries its cache key, so a cache hit is answered without staging a byte;
			// 			// anything older, or built from other input, is rebuilt.
			if !pi.NeedsBuild() {
				return nil
			}
			if key, keyErr := sm.projectBuildKey(context.Background(), project); keyErr == nil &&
				sm.ImageCarriesBuild(image, key) {
				return nil
			}
		}
		if pi.NeedsBuild() {
			ctx, cancel := context.WithTimeout(context.Background(), buildCeiling)
			defer cancel()
			if _, err := sm.BuildProjectImage(ctx, project, func(line string) {
				emit(types.SandboxImageStatus{Phase: phase, Line: line})
			}); err != nil {
				return fmt.Errorf("build sandbox image %s: %w", image, err)
			}
			return nil
		}
		if err := sm.PullImageWithProgress(image, func(line string) {
			emit(types.SandboxImageStatus{Phase: phase, Line: line})
		}); err != nil {
			return fmt.Errorf("pull sandbox image %s: %w", image, err)
		}
		return nil
	})
	if err != nil {
		emit(types.SandboxImageStatus{Phase: types.SandboxImagePhaseFailed, Error: err.Error()})
		return image, err
	}
	emit(types.SandboxImageStatus{Phase: types.SandboxImagePhaseDone})
	return image, nil
}

// doOnce runs fn once per key at a time, and a caller arriving mid-flight waits for that result instead of
// starting a second copy. Nothing is cached, so a failure can be retried.
func (sm *SandboxManager) doOnce(key string, fn func() error) (err error) {
	sm.imageOpsMu.Lock()
	if op, ok := sm.imageOps[key]; ok {
		sm.imageOpsMu.Unlock()
		op.wg.Wait()
		return op.err
	}
	op := &imageOp{}
	op.wg.Add(1)
	if sm.imageOps == nil {
		sm.imageOps = make(map[string]*imageOp)
	}
	sm.imageOps[key] = op
	sm.imageOpsMu.Unlock()

	// Published on the panic path too: a key left in flight would block every later caller for
	// the rest of the process.
	started := false
	defer func() {
		if started {
			op.err = err
		} else {
			op.err = fmt.Errorf("sandbox image preparation did not complete")
		}
		sm.finishImageOp(key, op)
	}()

	err = fn()
	started = true
	return err
}

// finishImageOp makes the result visible to waiters and forgets the in-flight key.
func (sm *SandboxManager) finishImageOp(key string, op *imageOp) {
	sm.imageOpsMu.Lock()
	if sm.imageOps[key] == op {
		delete(sm.imageOps, key)
	}
	sm.imageOpsMu.Unlock()
	op.wg.Done()
}
