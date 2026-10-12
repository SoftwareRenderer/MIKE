package extensions

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// refuseUninstallable prevents extensions from gaining host access before any manifest is read.
// It rejects unsafe directory names that could be target of os.RemoveAll.
// It also validates DockerfileURL because podman build's RUN steps execute on the host runtime,
// as --runtime does not affect the build process. Restore of an old index does not bypass this.
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
