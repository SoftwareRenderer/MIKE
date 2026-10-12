package extensions

// The active language is derived, never stored: the enabled translation pack
// that wins the ordering rule *is* the language, and no pack at all is English.
// There is no `language` config field and no `PUT /api/config` path for one —
// derived-only is the architecture, and it is what keeps the client's rendered
// language and the server's prompt locale from ever disagreeing.

// GetI18N returns the translation-pack declaration if present.
//
// The declaration, not the Type field, makes an extension a pack: Type
// is inferred for display and routing, so a manifest shipping a catalog
// alongside an LSP still carries a catalog. The frontend's resolveI18NPack
// tests the same thing (frontend/src/lib/i18n/load-pack.ts).
func (e *Extension) GetI18N() *I18NConfig {
	if e == nil {
		return nil
	}
	return e.Manifest.Capabilities.GetI18N()
}

// ActiveI18N returns the enabled translation pack defining the active language,
// or nil for English. The winner is the first enabled pack by priority then
// name, matching the frontend's resolution rule.
//
// The server-side prompt locale calls this to ensure the UI and prompts
// agree on the active pack.
func ActiveI18N(extensions []Extension) *Extension {
	var winner *Extension
	for i := range extensions {
		candidate := &extensions[i]
		if !candidate.Enabled || candidate.GetI18N() == nil {
			continue
		}
		if winner == nil || packsOrderedBefore(candidate, winner) {
			winner = candidate
		}
	}
	if winner == nil {
		return nil
	}
	// Hand back a copy: the caller keeps the answer even if it re-sorts or
	// reuses the slice it passed in.
	pack := *winner
	return &pack
}

// packsOrderedBefore reports whether a sorts ahead of b (priority ascending,
// then name ascending). Names are unique per author and the list holds one
// row each.
func packsOrderedBefore(a, b *Extension) bool {
	if a.Manifest.Priority != b.Manifest.Priority {
		return a.Manifest.Priority < b.Manifest.Priority
	}
	return a.Name < b.Name
}
