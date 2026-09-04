package extensions

// The active language is derived, never stored: the enabled translation pack
// that wins the ordering rule *is* the language, and no pack at all is English.
// There is no `language` config field and no `PUT /api/config` path for one —
// derived-only is the architecture, and it is what keeps the client's rendered
// language and the server's prompt locale from ever disagreeing.

// GetI18N returns this extension's translation-pack declaration, or nil when the
// extension declares none.
//
// The declaration, not the Type field, is what makes an extension a pack: Type
// is what inferExtensionType inferred for display and routing, so a manifest
// that ships a catalog alongside, say, an LSP still carries a catalog. The
// frontend's resolveI18NPack tests the same thing
// (frontend/src/lib/i18n/load-pack.ts) — the two ends of one rule, kept in step
// deliberately.
func (e *Extension) GetI18N() *I18NConfig {
	if e == nil {
		return nil
	}
	return e.Manifest.Capabilities.GetI18N()
}

// ActiveI18N returns the enabled translation pack that defines the active
// language, or nil when no pack is enabled — which means English.
//
// The winner is the first enabled pack ordered by manifest priority ascending,
// then by name ascending: the same rule HandleList sorts the extension list by
// and the same rule the frontend resolves with, so two enabled packs are a
// deterministic choice rather than a coin flip, and neither the API order nor a
// map's iteration order can change which language a user sees.
//
// A disabled pack never contributes text — it is skipped here exactly as the
// frontend skips it.
//
// This is the ONE derivation. The server-side prompt locale must call this
// rather than re-deriving the rule, or the UI and the prompts can disagree about
// which pack is active while both look correct.
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

// packsOrderedBefore reports whether a sorts ahead of b under the ordering rule
// (priority ascending, then name ascending). Equal on both means the same
// extension — names are unique per author and the list holds one row each.
func packsOrderedBefore(a, b *Extension) bool {
	if a.Manifest.Priority != b.Manifest.Priority {
		return a.Manifest.Priority < b.Manifest.Priority
	}
	return a.Name < b.Name
}
