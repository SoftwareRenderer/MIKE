package extensions

// Several paths ask the same question of a directory: what manifest.json does the extension installed
// here ship? The install paths, the registry update path and the startup repair all decode it, and all
// word the answer differently — which situation failed is the actionable half of each message, so the
// wording stays with the caller and only the reading lives here.
//
// Nothing in this file validates a manifest. A manifest is text from the registry, not a grant, and what
// a declaration may contain is manifest_validate.go's decision, taken later, on the paths that take one.

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// manifestDecodeError is a manifest.json that was read and did not parse, as against one that was not
// there to read. It adds nothing to the message of the error it carries, so a caller wrapping it with %w
// says its own sentence over the JSON error alone.
type manifestDecodeError struct{ err error }

func (e manifestDecodeError) Error() string { return e.err.Error() }
func (e manifestDecodeError) Unwrap() error { return e.err }

// readManifest reads and decodes dir/manifest.json. A read failure comes back as the filesystem gave it,
// so errors.Is(err, fs.ErrNotExist) still tells an absent install from an unreadable one; a parse
// failure reports through manifestDecodeError.
func readManifest(dir string) (ExtensionManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ExtensionManifest{}, err
	}
	var manifest ExtensionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ExtensionManifest{}, manifestDecodeError{err}
	}
	return manifest, nil
}

// isManifestDecode reports a readManifest failure as "the file was there and is not JSON".
func isManifestDecode(err error) bool {
	var decode manifestDecodeError
	return errors.As(err, &decode)
}

// manifestError words a readManifest failure for one situation: "local extension has no manifest.json"
// sends a person to the folder they pointed at, where "checked-out extension has no manifest.json" means
// the registry checkout is what broke.
func manifestError(err error, situation string) error {
	if isManifestDecode(err) {
		return fmt.Errorf("invalid manifest.json in %s: %w", situation, err)
	}
	return fmt.Errorf("%s has no manifest.json: %w", situation, err)
}
