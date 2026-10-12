package sandboxruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"mike/pkg/logging"
)

// The container probe. GVisorAvailable answers "is runsc installed"; this answers "did a gVisor
// container run", and a host can pass the first and fail the second. Boot keeps using GVisorAvailable,
// so nothing here starts a container on the way up — see
// `docs/sandbox-runtime.md → Two availability probes, one sweep`.

const (
	// ProbeTimeout bounds one probe. It sits well above a healthy create+start, because the first probe
	// on a fresh host may still be unpacking layers.
	ProbeTimeout = 90 * time.Second

	// probeContainerPrefix keeps this container out of the `kanban-sandbox-` family, which the startup
	// sweep and the e2e teardown delete by name.
	probeContainerPrefix = "mike-sandbox-probe"

	// probeCommand exits on its own, so `start -a` returns once the guest has run something.
	probeCommand = "/bin/true"

	// probeReasonMax caps the podman output that reaches a caller's message.
	probeReasonMax = 240
)

// ProbeSandbox starts one throwaway gVisor container of image and returns why this host cannot run
// one, or nil when it ran. Only the runtime and the storage layer are under test: the container mounts
// nothing and gets no capability or network.
//
// A missing image counts as usable — the store answered, so the sandbox works and the absent image is
// the caller's to report.
func (r *Runtime) ProbeSandbox(ctx context.Context, image string) error {
	if !GVisorAvailable("container") {
		return errors.New("runsc or podman is not installed or does not answer; the runtime log names which")
	}
	if err := ValidateImageRef(image); err != nil {
		return err
	}

	name := fmt.Sprintf("%s-%d", probeContainerPrefix, os.Getpid())
	var created bool
	defer func() {
		if created {
			if err := r.RemoveContainer(name); err != nil {
				logging.For(logging.Runtime).Warnf("probe container %s survived the probe: %v", name, err)
			}
		}
	}()

	createArgs := append(GVisorCreateArgs(name), "--network", "none", image, probeCommand)
	out, err := r.PodmanExecContext(ctx, createArgs...)
	if err != nil {
		if imgErr := ImageAvailabilityError(image, out); imgErr != nil {
			logging.For(logging.Runtime).Infof("sandbox probe: %v (the store answered, so the sandbox is usable)", imgErr)
			return nil
		}
		return fmt.Errorf("create probe container: %w (%s)", err, probeReason(out))
	}
	created = true

	// ignore-cgroups is a podman global option, so it precedes the verb.
	out, err = r.PodmanExecContext(ctx, "--runtime-flag", "ignore-cgroups=true", "start", "-a", name)
	if err != nil {
		return fmt.Errorf("start probe container: %w (%s)", err, probeReason(out))
	}
	return nil
}

// probeReason picks the line worth reading: podman prints a structured `level=error` line and then the
// bare error, and the bare error names the cause.
func probeReason(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fallback := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "time=") {
			return clip(line, probeReasonMax)
		}
		if fallback == "" {
			fallback = line
		}
	}
	if fallback == "" {
		return "no output"
	}
	return clip(fallback, probeReasonMax)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
