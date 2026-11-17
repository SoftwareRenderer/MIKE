package extensions

// Tests for the i18n translation-pack type: the manifest that declares a pack and
// the single derivation of which enabled pack *is* the active language. The
// declaration's refusals — the bare-subtag locale, the external or absolute or
// dot-segment catalog, the path bounds — are held by TestI18NConfigValidate and
// TestValidateI18NFiles, beside the validator they call.
//
// The ordering-rule test here (TestActiveI18N) and the "two enabled packs" case
// in frontend/src/lib/i18n/__tests__/load-pack.unit.svelte.ts run over the SAME
// three packs (pack-fr priority 10, pack-ja priority 1, pack-zh priority 1) and
// must agree that pack-ja wins. That agreement is the acceptance check that the
// rendered UI and the server-side prompt locale can never name different
// languages: both derive from this rule, and nothing stores the answer.
// Change the fixture in one and change it in the other.

import (
	"encoding/json/v2"
	"testing"
)

func TestI18NManifestJSONRoundTrip(t *testing.T) {
	raw := `{"display_name":"中文","author":"someone","version":"1.0.0",
		"capabilities":{"i18n":{"locale":"zh","messages_url":"messages.json","prompts_dir":"prompts"}}}`
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := m.Capabilities.GetI18N()
	if got == nil {
		t.Fatal("capabilities.i18n did not decode")
	}
	if got.Locale != "zh" || got.MessagesURL != "messages.json" || got.PromptsDir != "prompts" {
		t.Errorf("decoded %+v, want zh / messages.json / prompts", *got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the canonical shape must validate: %v", err)
	}
}

// i18nExt builds an extension row the way the store would hand one over.
func i18nExt(name string, priority int, enabled bool, locale string) Extension {
	return Extension{
		Name:    name,
		Enabled: enabled,
		Manifest: ExtensionManifest{
			Priority:     priority,
			DisplayName:  name,
			Capabilities: &Capabilities{I18N: &I18NConfig{Locale: locale, MessagesURL: "messages.json"}},
		},
	}
}

func TestActiveI18N(t *testing.T) {
	// No pack at all is English: nil, not an error and not a guess.
	if got := ActiveI18N([]Extension{{Name: "theme", Enabled: true}}); got != nil {
		t.Errorf("ActiveI18N with no pack = %+v, want nil", got)
	}
	if got := ActiveI18N(nil); got != nil {
		t.Errorf("ActiveI18N(nil) = %+v, want nil", got)
	}

	// A disabled pack never contributes text.
	disabled := []Extension{i18nExt("pack-zh", 0, false, "zh")}
	if got := ActiveI18N(disabled); got != nil {
		t.Errorf("a disabled pack must not be active, got %q", got.Name)
	}

	// Lower priority wins; ties break on name; and the input order must not
	// matter — the API's sort is a convenience, not the rule.
	for _, order := range [][]Extension{
		{i18nExt("pack-fr", 10, true, "fr"), i18nExt("pack-ja", 1, true, "ja"), i18nExt("pack-zh", 1, true, "zh")},
		{i18nExt("pack-zh", 1, true, "zh"), i18nExt("pack-fr", 10, true, "fr"), i18nExt("pack-ja", 1, true, "ja")},
		{i18nExt("pack-ja", 1, true, "ja"), i18nExt("pack-zh", 1, true, "zh"), i18nExt("pack-fr", 10, true, "fr")},
	} {
		got := ActiveI18N(order)
		if got == nil || got.Name != "pack-ja" {
			t.Errorf("ActiveI18N over a reordered list picked %+v, want pack-ja (priority 1, name tie)", got)
		}
	}

	// A disabled pack that would win is skipped, not promoted.
	mixed := []Extension{i18nExt("pack-ja", 0, false, "ja"), i18nExt("pack-fr", 9, true, "fr")}
	if got := ActiveI18N(mixed); got == nil || got.Name != "pack-fr" {
		t.Errorf("ActiveI18N with the winner disabled = %+v, want pack-fr", got)
	}

	// A non-pack extension is skipped even when enabled and first.
	mixed2 := []Extension{{Name: "aaa-theme", Enabled: true}, i18nExt("pack-zh", 5, true, "zh")}
	if got := ActiveI18N(mixed2); got == nil || got.Name != "pack-zh" {
		t.Errorf("ActiveI18N skipped the wrong rows: %+v", got)
	}
}

// The result is a copy: a caller that re-sorts or reuses its slice must not
// find the answer it was given changed underneath it.
func TestActiveI18N_ReturnsCopy(t *testing.T) {
	list := []Extension{i18nExt("pack-zh", 0, true, "zh")}
	got := ActiveI18N(list)
	if got == nil {
		t.Fatal("expected the only enabled pack to win")
	}
	list[0].Name = "mutated"
	list[0].Enabled = false
	if got.Name != "pack-zh" || !got.Enabled {
		t.Errorf("result tracks the caller's slice: %+v", got)
	}
	if got.Manifest.Capabilities.GetI18N().Locale != "zh" {
		t.Errorf("copied manifest lost its locale: %+v", got.Manifest.Capabilities)
	}
}
