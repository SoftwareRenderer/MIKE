package extensions

// The delete path (remove.go) treats an extension's directory as the extension itself, because
// that is what it is: UI code is served out of it, an agent adapter's WASM and binary load from
// it. The same fact has a second consequence — a row that says "installed" with no files behind
// it is an extension that silently does nothing while Settings still shows it enabled.
//
// This file is the other half of remove.go: a row outliving its files is detected once, at
// startup, and repaired from the commit the row itself was installed at.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mike/pkg/logging"
)

// How long one restore may take, and the whole pass. A boot runs this off the boot path, so the numbers
// only bound work the user is not waiting on — but an unbounded git fetch is a goroutine that outlives
// the reason it existed, so both are bounded.
const (
	repairPerExtensionTimeout = 2 * time.Minute
	repairPassTimeout         = 10 * time.Minute
)

var errRepairInFlight = errors.New("an operation on this extension is already running")

type missingInstall struct {
	ext        *Extension
	dir        string
	why        string
	repairable bool
}

// String renders the one-line form used in the startup log: the report has to be actionable from the
// log alone, because it names extensions that are enabled in the UI and doing nothing.
func (m missingInstall) String() string {
	return fmt.Sprintf("ext=%s dir=%s: %s", m.ext.ID, m.dir, m.why)
}

// The directory is derived from the stored record through extensionDir — the same derivation
// removeExtensionFiles deletes with, so the two can never disagree about where an extension lives. A
// name that cannot be a directory name is reported, not repaired: it would be a recursive write of
// third-party content outside the extensions root.
func (h *ExtensionHandler) classifyMissingInstalls() ([]missingInstall, error) {
	exts, err := h.store.List()
	if err != nil {
		return nil, fmt.Errorf("list extensions: %w", err)
	}
	root, err := filepath.Abs(h.config.ExtensionsDirOrDefault())
	if err != nil {
		return nil, fmt.Errorf("extensions directory %q could not be resolved: %w", h.config.ExtensionsDirOrDefault(), err)
	}

	var missing []missingInstall
	for i := range exts {
		ext := &exts[i]
		if reason := unsafeDirName(ext.Name); reason != "" {
			missing = append(missing, missingInstall{ext: ext, why: reason})
			continue
		}
		dir := extensionDir(root, ext.Author, ext.Name)
		why := installFilesMissing(dir)
		if why == "" {
			continue
		}
		m := missingInstall{ext: ext, dir: dir, why: why}
		switch reason := h.repairRefusal(ext, root, dir); reason {
		case "":
			m.repairable = true
		default:
			m.why = why + "; " + reason
		}
		missing = append(missing, m)
	}
	return missing, nil
}

func (h *ExtensionHandler) repairRefusal(ext *Extension, absRoot, dir string) string {
	if h.registry == nil {
		return "this server has no registry client, so nothing can be fetched"
	}
	if ext.Repo == "" || ext.Commit == "" {
		source := ext.InstallSource
		if source == "" {
			source = "unknown"
		}
		// A local install is materialized in place out of the extensions directory, so that directory
		// *was* the install: there is no second copy to restore it from, and inventing one would be a
		// guess about where the user's unpublished work lives.
		return fmt.Sprintf("installed from %s with no repo/commit on the record, so there is nothing to restore it from", source)
	}
	if err := validateGitSource(ext.Repo, ext.Commit); err != nil {
		return "git source on the record is unusable: " + err.Error()
	}
	if err := validateSubdir(ext.Subdir); err != nil {
		return "subdir on the record is unusable: " + err.Error()
	}
	// The delete path applies this containment before RemoveAll; the repair applies it before a
	// recursive copy.
	if !insideDir(absRoot, dir) {
		return fmt.Sprintf("%s is not inside the extensions directory %s", dir, absRoot)
	}
	return ""
}

// installFilesMissing reports why this directory is not a usable install, or "" when it is. A missing
// directory is the obvious case, but not the only one: a half-written checkout has a directory, and a
// manifest.json that does not parse is as unusable as none — scanLocalExtensions offers that same file
// up as "installable", so without it the directory is not an extension to either side.
func installFilesMissing(dir string) string {
	if _, err := readInstalledManifest(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "files are not installed"
		}
		return err.Error()
	}
	return ""
}

// readInstalledManifest reads the manifest that makes a directory an install. One derivation, so the
// walk that finds a missing install and the repair that checks what it restored ask the same question.
func readInstalledManifest(dir string) (*ExtensionManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("manifest.json could not be read: %w", err)
	}
	var manifest ExtensionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	return &manifest, nil
}

// restores one row's files from the commit that row was installed at — never a newer one, which
// is an update the user has to ask for.
//
// It runs on the handler's existing in-flight guard, so a repair cannot overlap the install or update of
// the same extension: both replace the whole directory, and an update that finished between the walk and
// the repair would otherwise be undone.
func (h *ExtensionHandler) repairOne(ctx context.Context, m missingInstall) error {
	if !h.inFlightOps.begin(m.ext.Name) {
		return errRepairInFlight
	}
	defer h.inFlightOps.end(m.ext.Name)

	if err := h.registry.CheckoutExtensionContext(ctx, m.ext.Repo, m.ext.Commit, m.ext.Subdir, m.dir); err != nil {
		return err
	}
	manifest, err := readInstalledManifest(m.dir)
	if err != nil {
		// The pinned commit no longer contains an extension where the record says one is. The directory
		// now holds that commit's bytes, which is exactly what an install of this row would have written,
		// but it is not something to call repaired.
		return fmt.Errorf("restored files are not an install: %w", err)
	}
	// The subdir a row points at can move inside its repository, and the record's author directory came
	// from the *registry entry*, which is not the same field as the manifest's author. So the comparison
	// that means something is manifest-vs-recorded-manifest: a restored manifest naming a different
	// author is a different extension, and serving it under the name the row was vetted under is exactly
	// what must not happen quietly.
	recorded := m.ext.Manifest.Author
	if manifest.Author != "" && recorded != "" && authorDir(manifest.Author) != authorDir(recorded) {
		return fmt.Errorf("restored files belong to %q, not the recorded extension author %q", manifest.Author, recorded)
	}
	return nil
}

// RepairMissingFiles restores installed extensions whose files are gone, and reports the rows that
// cannot be restored on their own. It is meant to be called once, when the app starts, off the boot
// path: with nothing missing it costs one stat per row and logs nothing.
//
// Repairs run one at a time — the walk normally finds nothing, and when it finds several the network is
// the last thing to hammer in parallel. The context bounds the pass; the caller is never joined to it,
// so a shutdown that arrives mid-clone still ends the process.
func (h *ExtensionHandler) RepairMissingFiles(ctx context.Context) (repaired, refused []string, err error) {
	missing, err := h.classifyMissingInstalls()
	if err != nil {
		return nil, nil, err
	}
	logMissing(missing)
	if len(missing) == 0 {
		return nil, nil, nil
	}

	passCtx, cancelPass := context.WithTimeout(ctx, repairPassTimeout)
	defer cancelPass()

	for _, m := range missing {
		if !m.repairable {
			continue // already in the log line above, with its reason
		}
		if err := passCtx.Err(); err != nil {
			refused = append(refused, m.ext.ID)
			logging.For(logging.Extensions).Infof("repair: ext=%s skipped — repair pass ended (%v)", m.ext.ID, err)
			continue
		}
		extCtx, cancelExt := context.WithTimeout(passCtx, repairPerExtensionTimeout)
		repErr := h.repairOne(extCtx, m)
		cancelExt()

		switch {
		case repErr == nil:
			repaired = append(repaired, m.ext.ID)
			logging.For(logging.Extensions).Infof("repair: ext=%s restored %s from %s@%s", m.ext.ID, m.dir, m.ext.Repo, short(m.ext.Commit))
		case errors.Is(repErr, errRepairInFlight):
			logging.For(logging.Extensions).Infof("repair: ext=%s skipped — an install or update is running", m.ext.ID)
		default:
			refused = append(refused, m.ext.ID)
			logging.For(logging.Extensions).Warnf("repair: ext=%s could not be restored from %s@%s: %v", m.ext.ID, m.ext.Repo, short(m.ext.Commit), repErr)
		}
	}

	// The files reappearing is a change to what the app can offer: an agent adapter's WASM and an llm
	// extension's mappings are read from disk, and the frontend has to re-read the injection list. One
	// call per pass, only when something was actually restored.
	if len(repaired) > 0 && h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
	logging.For(logging.Extensions).Warnf("repair: done — %d restored, %d could not be restored", len(repaired), len(refused))
	return repaired, refused, nil
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// describeMissing renders the rows that cannot self-heal into one log line each. Anything that stays
// broken after a repair pass has to be findable in the log, because the only thing the user sees is an
// enabled extension that does nothing.
func describeMissing(missing []missingInstall) string {
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		parts = append(parts, m.String())
	}
	return strings.Join(parts, "\n  - ")
}

// logMissing records what the walk found. Nothing is logged when nothing is missing: a healthy boot
// pays one stat per row and stays quiet about it.
func logMissing(missing []missingInstall) {
	if len(missing) == 0 {
		return
	}
	logging.For(logging.Extensions).Infof("repair: %d installed extension(s) have no files:\n%s", len(missing), describeMissing(missing))
}
