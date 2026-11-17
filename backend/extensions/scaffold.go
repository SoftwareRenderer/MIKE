package extensions

// Scaffolding creates the repository a custom extension begins in. Only the name comes from the
// request: the description belongs to the task's prompt, never to a served file, because this tree is
// public and has no session in front of it. The bundle is an fs.FS the caller injects — the binary
// embeds it — so this package never reaches for the source tree.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/fsutil"
)

// templateNameToken is the one substitution the bundle gets, written so no markdown or JSON file in it
// reads as a template engine's own syntax.
const templateNameToken = "{{NAME}}"

// maxTemplateFileBytes bounds one scaffolded file, so a bundle is bounded like every other generated tree.
const maxTemplateFileBytes = 256 << 10

// SetAuthoringBundle aims the create path at the bundle the running binary was built with. Without it a
// person cannot create an extension here.
func (h *ExtensionHandler) SetAuthoringBundle(bundle fs.FS) { h.authoring = bundle }

// ScaffoldDevRepo lays the authoring bundle down as <extensions>/_dev/<slug> and returns the files it
// wrote. The name is slugified here and nowhere else, so the directory, the project and the row cannot
// disagree about what the extension is called. A target that exists is refused rather than merged into.
func (h *ExtensionHandler) ScaffoldDevRepo(name string) (dir string, files []string, err error) {
	bundle := h.authoring
	if bundle == nil {
		return "", nil, fmt.Errorf("this build carries no authoring bundle, so no extension can be created")
	}
	slug, err := DevSlug(name)
	if err != nil {
		return "", nil, err
	}
	root, err := filepath.Abs(h.config.ExtensionsDirOrDefault())
	if err != nil {
		return "", nil, fmt.Errorf("the extensions directory %q could not be resolved: %w", h.config.ExtensionsDirOrDefault(), err)
	}
	dir = DevRepoDir(root, slug)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("an extension named %s already exists at %s", name, dir)
	}
	if _, err := fs.Stat(bundle, "README.md"); err != nil {
		// A bundle with no README is a build mistake, and scaffolding half of it would leave a
		// repository the agent has to guess the rules of.
		return "", nil, fmt.Errorf("the authoring bundle is incomplete: %w", err)
	}

	// The person's own words, not the directory name, are what the README and AGENTS.md speak with:
	// the slug is a path segment, and a folder called `my-cool-ext` should not read that way to a
	// human opening the repository.
	written, err := writeBundle(bundle, dir, name)
	if err != nil {
		// Nothing half-written survives a failed scaffold: the name stays free and the next attempt
		// starts from the same bundle.
		if cleanup := h.RemoveDevRepo(dir); cleanup != "" {
			return "", nil, fmt.Errorf("%s (and its removal left %s)", err, cleanup)
		}
		return "", nil, err
	}
	return dir, written, nil
}

// writeBundle copies the bundle under dst, filling the name into the two files that speak to a person.
// Dot-prefixed names are written — that is the point of the bundle's `.agents/` tree — and a name that
// escapes the target is refused before a byte is written.
func writeBundle(bundle fs.FS, dst, name string) ([]string, error) {
	var written []string
	err := fs.WalkDir(bundle, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(".", filepath.FromSlash(p))
		if err != nil || !fsutil.RelBelowRoot(rel) || rel == "." {
			return fmt.Errorf("%s is not a path inside the bundle", p)
		}
		f, err := bundle.Open(p)
		if err != nil {
			return fmt.Errorf("%s could not be read: %w", p, err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("%s could not be read: %w", p, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file", p)
		}
		body, err := io.ReadAll(io.LimitReader(f, maxTemplateFileBytes+1))
		if err != nil {
			return fmt.Errorf("%s could not be read: %w", p, err)
		}
		if len(body) > maxTemplateFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", p, maxTemplateFileBytes)
		}
		if strings.HasSuffix(rel, ".md") {
			body = []byte(strings.ReplaceAll(string(body), templateNameToken, name))
		}
		to := filepath.Join(dst, rel)
		// The bundle's own subtrees (.agents/skills/…, examples/…) do not exist yet, and an atomic
		// write lands its temp file beside the target rather than creating a path to it.
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return fmt.Errorf("%s could not be created: %w", filepath.Dir(to), err)
		}
		if err := fsutil.WriteFileAtomic(to, body); err != nil {
			return fmt.Errorf("%s could not be written: %w", to, err)
		}
		written = append(written, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(written) == 0 {
		return nil, fmt.Errorf("the authoring bundle holds no files")
	}
	return written, nil
}
