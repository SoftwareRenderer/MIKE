package main

import (
	"embed"
	"fmt"
	"io/fs"
)

// The prompt templates compiled into this binary. Not `all:prompts`, which would also carry
// `prompts/skills/` — an agent reads those through a container bind mount, never this process.
//
//go:embed prompts/*.tmpl
var embeddedPrompts embed.FS

// embeddedPromptTemplates roots the copy at its own file names, so an embedded template is keyed
// `base.tmpl` just as the disk file is. Both layers have to key names alike, or a shadowed template
// would parse twice under two spellings of itself.
func embeddedPromptTemplates() (fs.FS, error) {
	templates, err := fs.Sub(embeddedPrompts, "prompts")
	if err != nil {
		return nil, fmt.Errorf("prompt templates: %w", err)
	}
	return templates, nil
}
