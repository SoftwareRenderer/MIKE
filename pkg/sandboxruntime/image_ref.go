package sandboxruntime

import (
	"fmt"
	"strings"

	"mike/pkg/logging"
)

// Podman must never be handed an unqualified ("short") image name: it picks a registry by
// asking the user, and headless that fails -
//
//	Error: short-name resolution enforced but cannot prompt without a TTY
//
// "It usually works" is not a rule either (busybox:latest resolves, library/busybox:1.36
// does not), so Runtime.ResolveImageRef is the seam every image reference passes through.

// DefaultImageRegistry is the registry a docker-style short ref belongs to.
const DefaultImageRegistry = "docker.io"

// DefaultImageNamespace holds the single-component names ("ubuntu:24.04") that docker-style refs
// keep out of the registry path.
const DefaultImageNamespace = "library"

// LocalImageRegistry is where podman stores images built on this host: `podman build -t foo` tags
// the result `localhost/foo`. Everything MIKE builds is named under it.
const LocalImageRegistry = "localhost"

// IsQualifiedImageRef reports whether image already names its registry. The rule is docker's own:
// the first path component is a registry only when a "/" follows it and it holds a "." or a port,
// or is "localhost". A lone component is a repository name, however host-like it looks.
//
//	"docker.io/library/ubuntu:24.04" → true   "localhost/sandbox-mike" → true
//	"registry:5000/img:1"            → true   "myuser/img"             → false
//	"sandbox-getmike.dev"            → false  "sandbox-mike"           → false
func IsQualifiedImageRef(image string) bool {
	if image == "" {
		return false
	}
	// Strip the tag/digest so a port-like tail cannot be mistaken for a registry.
	name := image
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	first := name
	if slash := strings.Index(name, "/"); slash >= 0 {
		first = name[:slash]
	} else {
		// No "/": a repository name. "sandbox-getmike.dev" looks like a host and is not one —
		// that is the case that produced the reported error.
		return false
	}
	if colon := strings.Index(first, ":"); colon >= 0 {
		first = first[:colon]
	}
	if first == LocalImageRegistry {
		return true
	}
	return strings.ContainsAny(first, ".:")
}

// QualifyDockerImageRef adds the registry docker would use: "ubuntu:24.04" becomes
// "docker.io/library/ubuntu:24.04" and "myuser/img" becomes "docker.io/myuser/img". An already
// qualified ref is returned unchanged, and "" stays "" — an empty ref means "use the caller's
// default", which is not this function's business.
func QualifyDockerImageRef(image string) string {
	if image == "" || IsQualifiedImageRef(image) {
		return image
	}
	rest := ""
	if at := strings.Index(image, "@"); at >= 0 {
		rest, image = image[at:], image[:at]
	}
	tag := ""
	if colon := strings.LastIndex(image, ":"); colon >= 0 && !strings.Contains(image[colon:], "/") {
		tag, image = image[colon:], image[:colon]
	}
	prefix := DefaultImageRegistry + "/"
	if !strings.Contains(image, "/") {
		prefix += DefaultImageNamespace + "/"
	}
	return prefix + image + tag + rest
}

// QualifyLocalImageRef names a locally-built image the way podman stores it:
// "sandbox-getmike.dev" → "localhost/sandbox-getmike.dev".
func QualifyLocalImageRef(image string) string {
	if image == "" || IsQualifiedImageRef(image) {
		return image
	}
	return LocalImageRegistry + "/" + image
}

// ValidateConfiguredImageRef checks an image reference someone typed (a project's sandbox_image,
// an extension manifest's image): it must name its registry, so nothing downstream has to guess.
// Empty is valid — it means "not configured, use the default base image".
func ValidateConfiguredImageRef(image string) error {
	if image == "" {
		return nil
	}
	if err := ValidateImageRef(image); err != nil {
		return err
	}
	if !IsQualifiedImageRef(image) {
		return fmt.Errorf("sandbox image must be fully qualified, e.g. %s — got %q",
			QualifyDockerImageRef(image)+" or "+LocalImageRegistry+"/my-image", image)
	}
	return nil
}

// ResolveImageRef turns any reference into one podman can use without prompting: a qualified ref
// comes back unchanged with no podman call, a short one becomes "localhost/<name>" if that image
// exists locally and docker.io's interpretation otherwise. The local probe is for values stored
// before refs were qualified — "kanban-ext-gopls:1.0.0" is a local build, "myuser/img" is not.
func (r *Runtime) ResolveImageRef(image string) string {
	return resolveImageRefWith(image, r.imageExists)
}

// resolveImageRefWith is ResolveImageRef with the local-storage probe injected, so the decision is
// testable without podman or a registry.
func resolveImageRefWith(image string, exists func(string) bool) string {
	if image == "" || IsQualifiedImageRef(image) {
		return image
	}
	if local := QualifyLocalImageRef(image); exists != nil && exists(local) {
		return local
	}
	return QualifyDockerImageRef(image)
}

// ImageAvailabilityError turns podman's "could not get this image" output into something the user
// can act on, or nil when the output is unrelated and the caller should report podman's own error.
// Podman reports a missing local image as "image not known" and an unresolvable name as the TTY
// error; neither says the image was simply never built. The short-name case should be unreachable
// now that every ref is qualified — it stays as a tripwire.
func ImageAvailabilityError(image string, output []byte) error {
	out := string(output)
	switch {
	case strings.Contains(out, "short-name resolution"):
		return fmt.Errorf("sandbox image %q could not be resolved to a registry — this is a bug, every image reference must name its registry (see pkg/sandboxruntime/image_ref.go)", image)
	case strings.Contains(out, "image not known"),
		strings.Contains(out, "no such image"),
		strings.Contains(out, "unable to find image"):
		return fmt.Errorf("sandbox image %q is not available locally — build it from the project's Dockerfile or choose an image that exists (Settings → Sandbox)", image)
	case strings.Contains(out, "requested access to the resource is denied"),
		strings.Contains(out, "manifest unknown"),
		strings.Contains(out, "name unknown"):
		return fmt.Errorf("sandbox image %q could not be pulled — the registry has no such image or refuses access (Settings → Sandbox)", image)
	}
	return nil
}

// ImageExists reports whether image is in local storage, resolving it first. One call so callers
// don't re-implement the probe and drop the resolution or the flag-injection check on the floor.
func (r *Runtime) ImageExists(image string) bool {
	if ValidateImageRef(image) != nil {
		return false
	}
	return r.imageExists(r.ResolveImageRef(image))
}

// imageExists is the probe behind ResolveImageRef. `podman image exists` never contacts a
// registry, so it cannot prompt.
func (r *Runtime) imageExists(image string) bool {
	out, err := r.PodmanExec("image", "exists", image)
	if err != nil {
		if line := strings.TrimSpace(string(out)); line != "" {
			logging.For(logging.Runtime).Infof("image exists %s: %s", image, line)
		}
		return false
	}
	return true
}
