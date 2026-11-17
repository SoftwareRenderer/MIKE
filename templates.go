package main

import (
	"embed"
	"fmt"
	"io/fs"
)

// The authoring bundle a new extension repository is scaffolded from. `all:` because the bundle
// lives under `.agents/` — the project's own skill root, which the file server keeps private from
// the browser and the task container binds read-only, so this is the only way its files reach a
// machine that has no source tree.
//
//go:embed all:templates/extension
var embeddedExtensionTemplate embed.FS

// ExtensionAuthoringTemplate is the scaffold's source, rooted at the template directory itself so
// a scaffolded repository gets `README.md` at its root rather than `templates/extension/README.md`.
func ExtensionAuthoringTemplate() (fs.FS, error) {
	bundle, err := fs.Sub(embeddedExtensionTemplate, "templates/extension")
	if err != nil {
		return nil, fmt.Errorf("extension authoring template: %w", err)
	}
	return bundle, nil
}
