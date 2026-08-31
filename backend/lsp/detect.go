package lsp

// Language extensions know where their own dependencies live; core must not.
// So an extension may declare a probe — a short command to run in the sandbox
// the workspace is already building in — and core's entire job is to decide
// whether the answer is a directory the user already configured, and to hand it
// to that workspace's language-server session as an environment assignment.
//
// Two rules make this safe without core understanding a word of it:
//
//   - The probe runs where the workspace already runs. Never on the host, never
//     in the user's worktree from the outside, always one short command.
//   - The answer can only ever name a path inside a mount the user already gave
//     the sandbox. Detection chooses a value; it can never widen what is mounted.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/types"
)

const (
	// detectDefaultTimeout bounds one probe run when the manifest declares nothing.
	detectDefaultTimeout = 3 * time.Second
	// detectMaxTimeout is the hard ceiling on a probe. Detection happens on the
	// path to a turn's diagnostics, so a wedged task sandbox has to cost seconds.
	detectMaxTimeout = 5 * time.Second
	// detectMaxValueLen caps a probe's answer. A cache directory is a short path;
	// anything longer is a command that printed something we cannot use.
	detectMaxValueLen = 4096
	// detectRetryAfter is how long a failed detection is remembered. Cold caches
	// and unstarted containers are normal; re-probing per turn would put a probe
	// timeout in front of every diagnostics pass.
	detectRetryAfter = 5 * time.Minute
)

// errNoDetection is the shared "detection produced nothing" cause. Every path
// that produces it is a log line, never an error to the turn.
var errNoDetection = errors.New("no detection")

// TaskProber runs one short command in the sandbox container that already has
// workspacePath mounted. Implemented over SandboxManager.Exec in backend/sandbox
// and wired in main.go, so this package never imports the sandbox manager.
type TaskProber interface {
	ExecInTaskSandbox(ctx context.Context, workspacePath string, argv []string, timeout time.Duration) (string, error)
}

// SetTaskProber wires the host-side probe runner. Without it (no sandbox support,
// or an unwired test harness) detection is skipped and behaviour is what it has
// always been.
func (m *ExtensionLSPManager) SetTaskProber(p TaskProber) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.prober = p
	m.mu.Unlock()
}

// detection is one probe's outcome, remembered so a workspace pays for it once.
type detection struct {
	envs []string // "KEY=VALUE" assignments for this workspace's session
	// failedAt is set when the probe could not answer, so the next attempt waits
	// detectRetryAfter instead of stalling every pass on a dead container.
	failedAt time.Time
}

// expired reports whether a remembered failure is old enough to try again.
// A success never expires for the life of the process.
func (d *detection) expired(now time.Time) bool {
	return !d.failedAt.IsZero() && now.Sub(d.failedAt) >= detectRetryAfter
}

// detectEnvKey is the cache key: the project the workspace belongs to (its
// dependency root is shared by every worktree of it) plus the extension asking.
// Falls back to the workspace itself when the path is no registered project.
func detectEnvKey(projectPath, workspacePath, extensionID string) string {
	base := projectPath
	if base == "" {
		base = workspacePath
	}
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	return base + "\x00" + extensionID
}

// detectSessionEnvs resolves the env assignments this workspace's language server
// needs, by asking the workspace's own sandbox. Best-effort by construction: it
// returns nil (meaning "start the server exactly as before") on every failure,
// and each failure says why in the log once.
//
// containerMounts is the mount set the language server's sandbox was created
// with — the only surface it can see. An answer outside all of them is discarded,
// because a path the server cannot read is worse than no path: it produces
// confident "package not found" errors for code that is fine.
func (m *ExtensionLSPManager) detectSessionEnvs(ctx context.Context, ext *extensions.Extension, workspacePath string, containerMounts []types.SandboxMount) []string {
	if ext == nil || len(ext.SandboxConfig.Detect) == 0 {
		return nil
	}

	m.mu.Lock()
	prober := m.prober
	m.mu.Unlock()
	if prober == nil {
		log.Printf("[lsp-detect] ext=%s no task prober wired, skipping detection", ext.ID)
		return nil
	}

	key := detectEnvKey(m.projectPathFor(workspacePath), workspacePath, ext.ID)
	now := time.Now()
	m.mu.Lock()
	cached, seen := m.detections[key]
	m.mu.Unlock()
	if seen && !cached.expired(now) {
		return cached.envs
	}

	var envs []string
	for _, probe := range ext.SandboxConfig.Detect {
		argv, ok := probe.Argv()
		if !ok {
			log.Printf("[lsp-detect] ext=%s probe %q declares no command, skipping", ext.ID, probe.LogID())
			continue
		}
		value, err := m.runDetectProbe(ctx, prober, ext, probe, argv, workspacePath)
		if err != nil {
			log.Printf("[lsp-detect] ext=%s probe %q: %v", ext.ID, probe.LogID(), err)
			continue
		}
		resolved, err := applyDetectedValue(probe, value, containerMounts)
		if err != nil {
			log.Printf("[lsp-detect] ext=%s probe %q: %v", ext.ID, probe.LogID(), err)
			continue
		}
		envs = append(envs, resolved...)
	}

	m.mu.Lock()
	if len(envs) == 0 {
		m.detections[key] = detection{failedAt: now}
	} else {
		m.detections[key] = detection{envs: envs}
	}
	m.mu.Unlock()

	if len(envs) > 0 {
		log.Printf("[lsp-detect] ext=%s workspace=%s session env %s", ext.ID, workspacePath, strings.Join(envs, " "))
	}
	return envs
}

// runDetectProbe asks the workspace's sandbox one question and validates the
// answer. Detection is skipped for a workspace that has no sandbox container —
// which includes every project with sandboxing disabled, so an extension's
// command never runs on the host.
func (m *ExtensionLSPManager) runDetectProbe(ctx context.Context, prober TaskProber, ext *extensions.Extension, probe extensions.DetectProbe, argv []string, workspacePath string) (string, error) {
	if project := m.projectFor(workspacePath); project != nil && !project.SandboxEnabled {
		return "", fmt.Errorf("%w: project %s runs without a sandbox, so no probe runs", errNoDetection, project.ID)
	}
	out, err := prober.ExecInTaskSandbox(ctx, workspacePath, argv, probe.Timeout(detectDefaultTimeout, detectMaxTimeout))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoDetection, err)
	}
	return out, nil
}

// applyDetectedValue turns a probe's answer into env assignments, substituting
// {value} in each declared env.
func applyDetectedValue(probe extensions.DetectProbe, raw string, mounts []types.SandboxMount) ([]string, error) {
	value, hostPath, err := resolveDetectedPath(raw, mounts)
	if err != nil {
		return nil, err
	}
	// The path has to exist on the host too. The container path is mirrored from
	// this host path, so a directory that is not there means the mount is empty —
	// telling the server about it would only teach it to report missing packages.
	if info, err := os.Stat(hostPath); err != nil || !info.IsDir() {
		reason := "not a directory"
		if err != nil {
			reason = "not present on host"
		}
		return nil, fmt.Errorf("%w: %s is %s (%v)", errNoDetection, hostPath, reason, err)
	}
	var envs []string
	for _, e := range probe.Envs {
		if strings.TrimSpace(e.Key) == "" {
			continue
		}
		envs = append(envs, e.Key+"="+strings.ReplaceAll(e.Value, "{value}", value))
	}
	if len(envs) == 0 {
		return nil, fmt.Errorf("%w: probe %q declares no envs to assign", errNoDetection, probe.LogID())
	}
	return envs, nil
}

// resolveDetectedPath validates one probe answer and maps it from the container
// path the task sandbox reported to the host path that mount actually is.
//
// The answer is used as-is for the language server, because a project's Sandbox
// Configuration mounts are mirrored into the language server's sandbox at the
// same container path — the same string means the same directory in both.
func resolveDetectedPath(raw string, mounts []types.SandboxMount) (containerPath, hostPath string, err error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("%w: probe produced no output", errNoDetection)
	}
	if len(value) > detectMaxValueLen {
		return "", "", fmt.Errorf("%w: answer is %d bytes", errNoDetection, len(value))
	}
	if strings.ContainsAny(value, "\n\r") {
		// A probe that answers several lines is answering a different question.
		// Taking the first would be a guess.
		return "", "", fmt.Errorf("%w: answer is %d lines", errNoDetection, strings.Count(value, "\n")+1)
	}
	// ':' would rewrite a podman mount spec, and control characters are never part
	// of a path we were meant to mount.
	if strings.ContainsRune(value, ':') || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", "", fmt.Errorf("%w: answer has unusable characters", errNoDetection)
	}
	if !filepath.IsAbs(value) {
		return "", "", fmt.Errorf("%w: %q is not absolute", errNoDetection, value)
	}
	value = filepath.Clean(value)
	if value == "/" {
		return "", "", fmt.Errorf("%w: refusing the filesystem root", errNoDetection)
	}

	// Longest containing mount wins: a mount of /go/pkg/mod must be consulted
	// before a mount of /go, or the answer reverse-maps to the wrong host path.
	matched := -1
	for i, mo := range mounts {
		cp := filepath.Clean(strings.TrimSpace(mo.ContainerPath))
		if cp == "" || cp == "." {
			continue
		}
		if value == cp || strings.HasPrefix(value, cp+string(filepath.Separator)) {
			if matched < 0 || len(cp) > len(filepath.Clean(mounts[matched].ContainerPath)) {
				matched = i
			}
		}
	}
	if matched < 0 {
		return "", "", fmt.Errorf("%w: %s is outside every configured mount", errNoDetection, value)
	}
	mo := mounts[matched]
	rel, relErr := filepath.Rel(filepath.Clean(mo.ContainerPath), value)
	if relErr != nil {
		return "", "", fmt.Errorf("%w: %v", errNoDetection, relErr)
	}
	host := filepath.Join(filepath.Clean(mo.HostPath), rel)
	return value, host, nil
}

// projectFor returns the registered project a workspace path belongs to, or nil
// when it belongs to none (a path opened directly, or a project since removed).
func (m *ExtensionLSPManager) projectFor(workspacePath string) *types.Project {
	if m == nil || m.projects == nil {
		return nil
	}
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		abs = workspacePath
	}
	projects, err := m.projects.ListProjects()
	if err != nil {
		log.Printf("[lsp-detect] listing projects: %v", err)
		return nil
	}
	var best *types.Project
	bestLen := -1
	for i := range projects {
		p := filepath.Clean(projects[i].Path)
		if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
			// The deepest match is the owning project when paths nest.
			if len(p) > bestLen {
				best, bestLen = &projects[i], len(p)
			}
		}
	}
	return best
}

// projectPathFor returns the root of the project owning workspacePath, or "" —
// used only to key the detection cache so every worktree of one project shares
// one probe.
func (m *ExtensionLSPManager) projectPathFor(workspacePath string) string {
	if p := m.projectFor(workspacePath); p != nil {
		return p.Path
	}
	return ""
}
