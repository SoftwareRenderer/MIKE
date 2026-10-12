package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/llmadapter"
	"mike/pkg/logging"
)

const defaultMappingsFile = "mappings.json"

// CollectLLMMappings flattens mapping files of enabled `llm` extensions,
// ordered by (author, name). A broken mappings file must never take
// down LLM traffic, so malformed files are skipped with a warning.
func CollectLLMMappings(store *ExtensionStore, extensionsDir string) ([]llmadapter.Mapping, error) {
	exts, err := store.GetByType(ExtensionTypeLLM)
	if err != nil {
		return nil, fmt.Errorf("list llm extensions: %w", err)
	}
	enabled := make([]Extension, 0, len(exts))
	for _, e := range exts {
		if e.Enabled {
			enabled = append(enabled, e)
		}
	}
	sort.Slice(enabled, func(i, j int) bool {
		if enabled[i].Author != enabled[j].Author {
			return enabled[i].Author < enabled[j].Author
		}
		return enabled[i].Name < enabled[j].Name
	})

	var all []llmadapter.Mapping
	for i := range enabled {
		e := &enabled[i]
		file := defaultMappingsFile
		if cap := e.Manifest.Capabilities.GetLLM(); cap != nil && cap.MappingsFile != "" {
			file = cap.MappingsFile
		}
		path, ok := mappingsFilePath(extensionsDir, e.Author, e.Name, file)
		if !ok {
			logging.For(logging.Extensions).Warnf("llm extension %s/%s: invalid mappings_file %q, skipping", e.Author, e.Name, file)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			logging.For(logging.Extensions).Infof("llm extension %s/%s: cannot read mappings file %s: %v, skipping", e.Author, e.Name, path, err)
			continue
		}
		ms, err := llmadapter.MappingsFromJSON(data)
		if err != nil {
			logging.For(logging.Extensions).Warnf("llm extension %s/%s: invalid mappings file %s: %v, skipping", e.Author, e.Name, path, err)
			continue
		}
		all = append(all, ms...)
	}
	return all, nil
}

// extensionDir returns the on-disk directory of an extension: the standard
// author-namespaced layout, except that author "local" also falls back to the
// flat layout (extensionsDir/<name>) when the namespaced directory is absent
// — unpublished extensions are conventionally placed flat.
func extensionDir(extensionsDir, author, name string) string {
	standard := filepath.Join(extensionsDir, authorDir(author), name)
	if author == "local" {
		flat := filepath.Join(extensionsDir, name)
		if st, err := os.Stat(standard); err != nil || !st.IsDir() {
			if fst, ferr := os.Stat(flat); ferr == nil && fst.IsDir() {
				return flat
			}
		}
	}
	return standard
}

// mappingsFilePath resolves an extension's mapping file to <extension dir>/<file>,
// rejecting values that are absolute, empty, or escape the directory.
func mappingsFilePath(extensionsDir, author, name, file string) (string, bool) {
	file = strings.TrimSpace(file)
	if file == "" || filepath.IsAbs(file) || strings.Contains(file, "..") {
		return "", false
	}
	dir := extensionDir(extensionsDir, author, name)
	full := filepath.Join(dir, file)
	// Re-containment check after Join (which collapses lexical .. segments).
	rel, err := filepath.Rel(dir, filepath.Clean(full))
	if err != nil || !fsutil.RelBelowRoot(rel) {
		return "", false
	}
	return filepath.Clean(full), true
}
