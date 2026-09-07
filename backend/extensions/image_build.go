package extensions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kanban/pkg/sandboxruntime"
)

// An extension image is built in a temp directory this package creates, never in the server's
// working directory. Two reasons:
//
//   - The Dockerfiles copy the bridge out of the context (COPY sandbox/sandbox-bridge ...), so the
//     context must contain that path. Building from the working directory supplied it by luck; the
//     app image's .dockerignore excluded that exact path (correctly, for that build) and every
//     language-server image failed to build.
//   - The Dockerfile comes from a registry. With the repo root as context, COPY data/kanban.db or
//     COPY .git would succeed, stopped only by .dockerignore lines written for a different build.
const (
	// extensionBuildContextBridge is the path inside the context that extension Dockerfiles copy.
	// It is a contract with published extensions, not a layout choice.
	extensionBuildContextBridge = "sandbox/sandbox-bridge"
	// extensionBuildContextDirPrefix prefixes the temp directory holding the context.
	extensionBuildContextDirPrefix = "kanban-ext-build-"
)

// buildExtensionImageArgs is the podman argv for an extension build. Pure, so tests can pin the shape.
func buildExtensionImageArgs(imageRef, contextDir string) []string {
	return []string{"build", "-t", imageRef, "-f", filepath.Join(contextDir, "Dockerfile"), contextDir}
}

// BuildExtensionImage builds an extension's image from a Dockerfile's text and tags it imageRef.
//
// No context is taken, on purpose: install and update run detached from the request so they finish
// after the client disconnects (see streamOperation). The bound that matters is here - the process
// carries PodmanWaitDelay, so a wedged build cannot hold its output pipes past shutdown.
func (rc *RegistryClient) BuildExtensionImage(dockerfileContent, imageRef string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	if strings.TrimSpace(dockerfileContent) == "" {
		return fmt.Errorf("build image %s: the extension's Dockerfile is empty", imageRef)
	}
	// A short ref means the local build; resolve it before staging (see image_ref.go).
	imageRef = rc.rt.ResolveImageRef(imageRef)

	bridgePath, err := rc.extensionBridgeBinary()
	if err != nil {
		return err
	}

	contextDir, err := os.MkdirTemp("", extensionBuildContextDirPrefix)
	if err != nil {
		return fmt.Errorf("create build context: %w", err)
	}
	defer os.RemoveAll(contextDir)

	if err := stageExtensionBuildContext(contextDir, dockerfileContent, bridgePath); err != nil {
		return fmt.Errorf("stage build context for %s: %w", imageRef, err)
	}

	cmd := sandboxruntime.PodmanCommand(buildExtensionImageArgs(imageRef, contextDir)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build image %s: %w (output: %s)", imageRef, err, string(output))
	}
	return nil
}

// extensionBridgeBinary finds the bridge to stage. It is the same binary Create later copies to
// /sandbox-bridge, so a built image is never newer than the server that ran the build.
//
// Resolved up front because otherwise a missing bridge fails inside buildah as a glob error naming a
// temp directory nobody is looking at.
func (rc *RegistryClient) extensionBridgeBinary() (string, error) {
	p, err := rc.rt.GetAgentBinaryPath()
	if err != nil {
		return "", fmt.Errorf("no sandbox-bridge to build the extension image with: %w", err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox-bridge path %s: %w", p, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("sandbox-bridge %s is not readable: %w", abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("sandbox-bridge path %s is a directory, not a binary", abs)
	}
	return abs, nil
}

// stageExtensionBuildContext writes the two files the build needs: the Dockerfile, and the bridge at
// extensionBuildContextBridge. It writes no .dockerignore - there is nothing here to protect, and an
// ignore rule meant for one build deciding another's inputs is what broke every language server.
func stageExtensionBuildContext(contextDir, dockerfileContent, bridgePath string) error {
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfileContent), 0o644); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}

	bridgeDir, _ := filepath.Split(extensionBuildContextBridge)
	if err := os.MkdirAll(filepath.Join(contextDir, filepath.FromSlash(bridgeDir)), 0o755); err != nil {
		return fmt.Errorf("create %s in the build context: %w", filepath.Dir(extensionBuildContextBridge), err)
	}

	src, err := os.Open(bridgePath)
	if err != nil {
		return fmt.Errorf("open sandbox-bridge %s: %w", bridgePath, err)
	}
	defer src.Close()

	dstPath := filepath.Join(contextDir, filepath.FromSlash(extensionBuildContextBridge))
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create sandbox-bridge in the build context: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("copy sandbox-bridge into the build context: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close sandbox-bridge in the build context: %w", err)
	}
	// Set, not inherited: COPY preserves the mode, and a bridge without +x makes an unusable image.
	if err := os.Chmod(dstPath, 0o755); err != nil {
		return fmt.Errorf("mark sandbox-bridge executable in the build context: %w", err)
	}
	return nil
}
