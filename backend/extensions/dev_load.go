package extensions

// Loading a development extension is how a person tests what their agent wrote. One rule decides the
// bytes: the row loads from a directory derived from stored rows — the repository when no task is named,
// the named task's worktree otherwise. Bytes outside the served root are copied into the staging tree
// first, and this is the only load path: Try Extension passes a task id, the Extensions card's Reload
// passes none, and both arrive here.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mike/backend/errs"
	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/logging"
)

const (
	// maxDevFileBytes caps one loaded file. An extension asset is source the browser parses, not data.
	maxDevFileBytes = 2 << 20
	// maxDevTotalBytes caps one loaded version, so a worktree full of build output cannot fill the
	// extensions volume.
	maxDevTotalBytes = 16 << 20
	// maxDevFiles caps how many names one load copies.
	maxDevFiles = 256
)

// loadedSkip holds the names that are never part of an extension and are not dot-prefixed, so the
// dot-segment rule in loadedNames does not already cover them.
var loadedSkip = map[string]bool{
	"node_modules": true,
}

// DevSources is what the load path needs to turn a stored row into bytes on disk. Every directory it
// hands back is derived from a row this server wrote, never from a request.
type DevSources interface {
	GetProject(id string) (*types.Project, error)
	ListProjects() ([]types.Project, error)
	GetTask(id string) (*types.Task, error)
	WorktreePath(taskID string) (string, error)
}

// SetDevSources aims the load path at the project table and the worktree layout. Without it a
// development row cannot load.
func (h *ExtensionHandler) SetDevSources(src DevSources) { h.devSources = src }

// LoadDev points one development row at a version and makes it the running one: an empty taskID loads
// the repository, a taskID of the row's project loads that task's worktree. A manifest that will not
// validate, or that consentRefusal() rejects because nobody has acknowledged what it asks for, leaves the
// running version in place; `ack` is the fingerprint the browser echoes back after the dialog.
func (h *ExtensionHandler) LoadDev(id, taskID, ack string) (*Extension, error) {
	ext, err := h.store.GetByID(id)
	if err != nil {
		return nil, err
	}
	if !ext.IsDev() {
		return nil, fmt.Errorf("%s is not a development extension, so nothing can be loaded into it", id)
	}
	if h.devSources == nil {
		return nil, errors.New("this server cannot read a project's files, so no development version can be loaded")
	}
	root, err := filepath.Abs(h.config.ExtensionsDirOrDefault())
	if err != nil {
		return nil, fmt.Errorf("the extensions directory %q could not be resolved: %w", h.config.ExtensionsDirOrDefault(), err)
	}

	src, slug, err := h.devSourceDir(root, ext, taskID)
	if err != nil {
		return nil, err
	}

	manifest, err := readManifest(src)
	if err != nil {
		return nil, manifestError(err, "development extension")
	}

	// Asked before anything is copied or written, so a refused version is never served. Whether the
	// extension may reach the network is the person's call, not the task's.
	fingerprint, err := consentRefusal(ext, &manifest, ack)
	if err != nil {
		return nil, err
	}

	dst := src
	loadedRel, err := filepath.Rel(root, src)
	if err != nil {
		return nil, fmt.Errorf("%s is not inside the extensions directory %s: %w", src, root, err)
	}
	if !underDevRoot(root, src) || isStagingSegment(root, src) {
		// The task's worktree: outside everything the browser may reach, so its bytes need a served
		// home of their own, named by the task whose work they are.
		dst, err = DevStagingDir(root, slug, taskID)
		if err != nil {
			return nil, err
		}
		if err := copyLoadedFiles(src, dst); err != nil {
			return nil, err
		}
		if loadedRel, err = filepath.Rel(root, dst); err != nil {
			return nil, fmt.Errorf("%s is not inside the extensions directory %s: %w", dst, root, err)
		}
	}

	// The same list an install applies, run against the directory that will actually be served.
	if _, err := h.validateLoadedManifest(ext.Name, &manifest, dst); err != nil {
		return nil, err
	}

	ext.Manifest = manifest
	ext.Version = manifest.Version
	ext.Type = inferExtensionType(manifest.Capabilities)
	ext.LoadedDir = filepath.ToSlash(loadedRel)
	ext.TaskID = taskID
	ext.Enabled = true
	// The acknowledgement describes these bytes, so it travels with them. A version that asks for
	// nothing stores none.
	ext.NetworkAck = fingerprint
	if err := h.store.SetLoaded(ext); err != nil {
		return nil, err
	}
	logging.For(logging.Extensions).Infof("ext=%s loaded its development version from %s", ext.ID, src)
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
	return ext, nil
}

// devSourceDir is the one place a development row's bytes are located. The repository is the project's
// stored path, which must sit in the namespace; the task's worktree is that task's, and only a task of
// the same project has one.
func (h *ExtensionHandler) devSourceDir(root string, ext *Extension, taskID string) (dir, slug string, err error) {
	project, err := h.devSources.GetProject(ext.ProjectID)
	if err != nil {
		return "", "", fmt.Errorf("the project this extension was created for is gone: %w", err)
	}
	repo, err := filepath.Abs(project.Path)
	if err != nil {
		return "", "", fmt.Errorf("%q could not be resolved: %w", project.Path, err)
	}
	if !underDevRoot(root, repo) {
		return "", "", fmt.Errorf("%s is not a development repository, so nothing can be loaded from it", project.Path)
	}
	slug = filepath.Base(repo)
	if reason := unsafeDirName(slug); reason != "" {
		return "", "", fmt.Errorf("the development directory %q is unusable: %s", slug, reason)
	}
	if taskID == "" {
		return repo, slug, nil
	}
	task, err := h.devSources.GetTask(taskID)
	if err != nil {
		return "", "", fmt.Errorf("%s is not a task: %w", taskID, err)
	}
	if task.ProjectID != ext.ProjectID {
		return "", "", fmt.Errorf("%s belongs to another project, so its worktree cannot be loaded here", taskID)
	}
	worktree, err := h.devSources.WorktreePath(taskID)
	if err != nil {
		return "", "", fmt.Errorf("%s has no worktree to load: %w", taskID, err)
	}
	if info, err := os.Stat(worktree); err != nil || !info.IsDir() {
		// Never fall back to the repository: showing merged bytes to someone reading the task's would
		// be worse than refusing.
		return "", "", fmt.Errorf("%s has no worktree to load - load the repository version instead", taskID)
	}
	return worktree, slug, nil
}

// copyLoadedFiles lays one version's files down in dst, which a browser is then served without a session.
// The copy takes the directory's own files plus what the manifest resolves, because a manifest names its
// stylesheet and its catalog but never the script its own index.html loads.
func copyLoadedFiles(src, dst string) error {
	names, declared, err := loadedNames(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("cannot prepare %s: %w", tmp, err)
	}
	var total int64
	for _, rel := range names {
		info, err := os.Lstat(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%s could not be read: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 && !declared[rel] {
			continue
		}
		if !info.Mode().IsRegular() {
			// A link the manifest named reaches bytes outside the extension, and anything else in a
			// file's seat is a socket or a device.
			return fmt.Errorf("%s is not a file", rel)
		}
		if info.Size() > maxDevFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", rel, maxDevFileBytes)
		}
		total += info.Size()
		if total > maxDevTotalBytes {
			return fmt.Errorf("%s carries more extension files than one load can take", src)
		}
		if err := copyOneLoadedFile(src, tmp, rel, info.Size()); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("the loaded copy at %s could not be replaced: %w", dst, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("the loaded copy could not be put in place: %w", err)
	}
	return nil
}

// loadedNames splits what a load copies in two: the directory's own files, and the names a manifest
// resolves against it. A link among the first is noise a worktree carries; a link where the manifest
// says a file is gets refused, because the served copy would hold the wrong bytes.
func loadedNames(src string) (names []string, declared map[string]bool, err error) {
	declared = map[string]bool{}
	add := func(rel string, on bool) error {
		if rel == "" {
			return nil
		}
		if err := fsutil.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("%s is not a path inside the extension: %w", rel, err)
		}
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(seg, ".") || loadedSkip[seg] {
				return nil
			}
		}
		names = append(names, rel)
		if on {
			declared[rel] = true
		}
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, nil, fmt.Errorf("%s could not be read: %w", src, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := add(e.Name(), false); err != nil {
			return nil, nil, err
		}
	}
	manifest, err := readManifest(src)
	if err != nil {
		return nil, nil, manifestError(err, "development extension")
	}
	if caps := manifest.Capabilities; caps != nil {
		if caps.I18N != nil {
			if err := add(caps.I18N.MessagesURL, true); err != nil {
				return nil, nil, err
			}
			if caps.I18N.PromptsDir != "" {
				sub, err := os.ReadDir(filepath.Join(src, filepath.FromSlash(caps.I18N.PromptsDir)))
				if err != nil {
					return nil, nil, fmt.Errorf("the pack's prompt directory could not be read: %w", err)
				}
				for _, e := range sub {
					if e.IsDir() {
						continue
					}
					if err := add(filepath.ToSlash(filepath.Join(caps.I18N.PromptsDir, e.Name())), true); err != nil {
						return nil, nil, err
					}
				}
			}
		}
		if caps.CSS != nil {
			if err := add(caps.CSS.CSSURL, true); err != nil {
				return nil, nil, err
			}
		}
		if caps.LLM != nil {
			if err := add(caps.LLM.MappingsFile, true); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(names) > maxDevFiles {
		return nil, nil, fmt.Errorf("%s carries more than %d files", src, maxDevFiles)
	}
	return names, declared, nil
}

func copyOneLoadedFile(src, dst, rel string, size int64) error {
	to := filepath.Join(dst, filepath.FromSlash(rel))
	// The same rule as ValidateRelPath, against the directory being written into: a name that survives
	// both cannot leave it.
	if relPath, err := filepath.Rel(dst, to); err != nil || !fsutil.RelBelowRoot(relPath) {
		return fmt.Errorf("%s would be written outside the loaded copy", rel)
	}
	in, info, err := fsutil.OpenChecked(filepath.Join(src, filepath.FromSlash(rel)))
	if err != nil {
		return fmt.Errorf("%s could not be opened: %w", rel, err)
	}
	defer in.Close()
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(in, size+1)); err != nil {
		return fmt.Errorf("%s could not be copied: %w", rel, err)
	}
	return nil
}

// validateLoadedManifest runs over a manifest, and the directory it will be served from, every check an
// install runs, so "it installed" and "it loaded" cannot mean different things. It returns the sandbox
// configuration the manifest implies, which is what an install stores beside the row.
func (h *ExtensionHandler) validateLoadedManifest(name string, manifest *ExtensionManifest, dir string) (SandboxConfig, error) {
	// Manifest URLs are third-party text: no javascript: or data: reaches a browser.
	sanitizeManifestURLs(manifest)

	// A manifest may not grant itself mounts outside the app's own roots or capabilities that reach past
	// the container; the declaration is rejected rather than stored and skipped at every start.
	sandboxCfg := sandboxConfigFromManifest(lspSandboxFromManifest(manifest))
	if err := h.mountPolicy().ValidateSandboxAdditions(sandboxCfg.Mounts, sandboxCfg.Capabilities); err != nil {
		return sandboxCfg, fmt.Errorf("extension %s declares an unusable sandbox configuration: %w", name, err)
	}
	if err := manifest.Capabilities.GetLSP().Validate(); err != nil {
		return sandboxCfg, fmt.Errorf("extension %s declares an unusable language configuration: %w", name, err)
	}
	// Checked against the files that will actually be served, so a bad locale, an unparsable catalog or a
	// prompt overlay that will not compile is refused here rather than in the browser.
	if err := validateI18N(manifest.Capabilities, dir); err != nil {
		return sandboxCfg, fmt.Errorf("extension %s declares an unusable translation pack: %w", name, err)
	}
	// The stylesheet gets the same containment as the pack's files: the browser resolves it into a
	// <link> before anything here can look again.
	if err := manifest.Capabilities.GetCSS().Validate(); err != nil {
		return sandboxCfg, fmt.Errorf("extension %s declares an unusable stylesheet: %w", name, err)
	}
	// The declaration is text from the browser, so it is vetted before it is stored.
	if err := validateTools(manifest.Capabilities, manifest.ConfigSchema); err != nil {
		return sandboxCfg, fmt.Errorf("extension %s declares an unusable tool: %w", name, err)
	}
	return sandboxCfg, nil
}

// consentAnswer is a refused load's answer: what the install dialog renders, plus the fingerprint a
// retry echoes back. The capabilities come from the server, so the dialog describes the bytes about to
// run rather than the last ones that did.
type consentAnswer struct {
	ConsentRequired bool                 `json:"consent_required"`
	Entry           consentEntry         `json:"entry"`
	AgentMounts     []types.SandboxMount `json:"agent_mounts,omitzero"`
	Fingerprint     string               `json:"fingerprint"`
	Reasons         []string             `json:"reasons"`
}

// consentEntry is the dialog's entry row. It carries a whole registry entry because that is the shape
// the dialog's prop asks for, and only the server knows the version about to run.
type consentEntry struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"display_name"`
	Version      string        `json:"version"`
	Description  string        `json:"description"`
	Author       string        `json:"author"`
	Type         string        `json:"type"`
	Capabilities *Capabilities `json:"capabilities,omitzero"`
}

// consentAnswerOf renders a refusal.
func consentAnswerOf(refused *ErrConsentRequired) consentAnswer {
	answer := consentAnswer{
		ConsentRequired: true,
		Entry: consentEntry{
			Name:         refused.Name,
			DisplayName:  refused.Manifest.DisplayName,
			Version:      refused.Manifest.Version,
			Description:  refused.Manifest.Description,
			Author:       refused.Manifest.Author,
			Type:         inferExtensionType(refused.Manifest.Capabilities),
			Capabilities: refused.Manifest.Capabilities,
		},
		Fingerprint: refused.Fingerprint,
		Reasons:     refused.Reasons(),
	}
	if caps := refused.Manifest.Capabilities; caps != nil && caps.Agent != nil && caps.Agent.Sandbox != nil {
		for _, m := range caps.Agent.Sandbox.Mounts {
			if m.HostPath != "" && m.ContainerPath != "" {
				answer.AgentMounts = append(answer.AgentMounts, m)
			}
		}
	}
	return answer
}

// DevResponse answers the create path about the row it just made, in the shape the listing uses. That
// row has no container yet, so there is no sandbox status to report.
func (h *ExtensionHandler) DevResponse(ext *Extension) ExtensionResponse {
	return h.toResponse(ext, nil)
}

// devNameTaken is the refusal for a name another row holds: MIKE's sentence, never driver text.
func devNameTaken(slug string) error {
	return errs.Conflict(fmt.Sprintf("an extension named %s already exists — delete it in Settings → Extensions, or choose another name", slug))
}

// devRowIsLeftover reports whether a development row is the remainder of a create that never landed: no
// repository, no project naming it or holding its path, and nothing configured or stored in it.
func (h *ExtensionHandler) devRowIsLeftover(slug string, row *Extension) (bool, error) {
	root, err := filepath.Abs(h.config.ExtensionsDirOrDefault())
	if err != nil {
		return false, fmt.Errorf("the extensions directory %q could not be resolved: %w", h.config.ExtensionsDirOrDefault(), err)
	}
	repo := DevRepoDir(root, slug)
	if _, err := os.Stat(repo); err == nil {
		// A repository on disk means the row was uninstalled, not abandoned.
		return false, nil
	}
	if h.devSources == nil {
		// Without the project table nothing here can call a row abandoned.
		return false, nil
	}
	if row.ProjectID != "" {
		if _, err := h.devSources.GetProject(row.ProjectID); err == nil {
			return false, nil
		} else if !errs.IsNotFound(err) {
			return false, err
		}
	}
	projects, err := h.devSources.ListProjects()
	if err != nil {
		return false, err
	}
	for _, p := range projects {
		if abs, err := filepath.Abs(p.Path); err == nil && abs == repo {
			return false, nil
		}
	}
	if row.IsLoaded() || row.Enabled || row.TaskID != "" || row.NetworkAck != "" || len(row.Config) > 0 {
		return false, nil
	}
	stored, err := h.store.StorageCountAll(row.ID)
	if err != nil {
		return false, err
	}
	return stored == 0, nil
}

// ReserveDevName is the create path's name check before it writes a byte. A row something still owns keeps
// the name and the create is refused; a row nothing owns is deleted, which is safe because devRowIsLeftover
// said it holds nothing.
func (h *ExtensionHandler) ReserveDevName(name string) error {
	slug, err := DevSlug(name)
	if err != nil {
		return err
	}
	row, err := h.store.GetByAuthorName(devAuthor, slug)
	if errs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	leftover, err := h.devRowIsLeftover(slug, row)
	if err != nil {
		return err
	}
	if !leftover {
		return devNameTaken(slug)
	}
	files, rowErr := h.deleteExtension(row.ID, row)
	if rowErr != nil {
		return fmt.Errorf("the leftover extension row %s could not be deleted: %w", row.ID, rowErr)
	}
	if files != "" {
		logging.For(logging.Extensions).Warnf("ext=%s LEFTOVER FILES: %s", row.ID, files)
	}
	logging.For(logging.Extensions).Infof("ext=%s dropped the leftover development row named %s: no project and no repository claimed it", row.ID, slug)
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
	return nil
}

// CreateDevRow creates the unloaded row that stands beside an authoring repository: no manifest, not
// enabled. The (author, name) index is what refuses a name another row holds.
func (h *ExtensionHandler) CreateDevRow(name, projectID string) (*Extension, error) {
	slug, err := DevSlug(name)
	if err != nil {
		return nil, err
	}
	ext := &Extension{
		ID:            fmt.Sprintf("dev-%s-%d", slug, time.Now().Unix()),
		Name:          slug,
		Author:        devAuthor,
		InstallSource: DevInstallSource,
		ProjectID:     projectID,
	}
	if err := h.store.Create(ext); err != nil {
		if isDuplicateName(err) {
			return nil, devNameTaken(slug)
		}
		return nil, err
	}
	// The row is a change like any other: without this broadcast the browser's app-wide copy of the
	// list keeps the snapshot it took at boot, and a task of this project shows no Try Extension
	// until the page is reloaded.
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
	return ext, nil
}

// PruneDevStaging removes a deleted task's loaded copy, so a task that is gone leaves nothing being
// served in its name. A row that was pointing at it reports itself unloaded.
func (h *ExtensionHandler) PruneDevStaging(taskID string) {
	if taskID == "" {
		return
	}
	exts, err := h.store.GetByType("")
	if err != nil {
		return
	}
	for i := range exts {
		ext := &exts[i]
		if !ext.IsDev() || ext.TaskID != taskID {
			continue
		}
		if leftover := h.removeDevStaging(ext); leftover != "" {
			logging.For(logging.Extensions).Warnf("ext=%s LEFTOVER FILES: %s", ext.ID, leftover)
		}
		ext.TaskID = ""
		ext.LoadedDir = ""
		ext.Enabled = false
		if err := h.store.SetLoaded(ext); err != nil {
			logging.For(logging.Extensions).Warnf("ext=%s could not be marked unloaded: %v", ext.ID, err)
			continue
		}
		if h.onExtensionChanged != nil {
			h.onExtensionChanged()
		}
	}
}
