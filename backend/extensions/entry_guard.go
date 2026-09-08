package extensions

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// refuseUninstallable judges the two index fields that reach the host before any manifest is
// read. Name derives the directory the checkout is os.RemoveAll'd into, and unsafeDirName is
// the rule the delete path already holds a stored record to. DockerfileURL is fetched and
// handed to podman build, whose RUN steps run on the host runtime — no --runtime reaches
// podman build. A row or index restored from before this rule is not consent.
func refuseUninstallable(entry *RegistryEntry) error {
	if reason := unsafeDirName(entry.Name); reason != "" {
		return fmt.Errorf("registry entry %s", reason)
	}
	if entry.DockerfileURL != "" {
		if err := validateRemoteAssetURL("dockerfile_url", entry.DockerfileURL); err != nil {
			return err
		}
	}
	return nil
}

// validateRemoteAssetURL requires http(s) and refuses an absolute path: a registry base may
// legitimately be file://, and ResolveURL turns "/etc/shadow" against it into a host read.
func validateRemoteAssetURL(field, raw string) error {
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, "/") || filepath.VolumeName(raw) != "" {
			return fmt.Errorf("registry entry %s must be a path inside the registry, not an absolute path (%q)", field, raw)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("registry entry %s is not a URL: %w", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("registry entry %s must be an http(s) URL, not %q", field, u.Scheme)
	}
	return nil
}
