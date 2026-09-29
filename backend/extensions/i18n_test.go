package extensions

// Tests for the i18n translation-pack type: the manifest that declares a pack,
// the validation that refuses an unloadable one, and the single derivation of
// which enabled pack *is* the active language.
//
// The ordering-rule test here (TestActiveI18N) and the "two enabled packs" case
// in frontend/src/lib/i18n/__tests__/load-pack.unit.svelte.ts run over the SAME
// three packs (pack-fr priority 10, pack-ja priority 1, pack-zh priority 1) and
// must agree that pack-ja wins. That agreement is the acceptance check that the
// rendered UI and the server-side prompt locale can never name different
// languages: both derive from this rule, and nothing stores the answer.
// Change the fixture in one and change it in the other.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInferExtensionType_I18N(t *testing.T) {
	caps := &Capabilities{I18N: &I18NConfig{Locale: "zh", MessagesURL: "messages.json"}}
	if got := inferExtensionType(caps); got != ExtensionTypeI18N {
		t.Errorf("inferExtensionType(i18n) = %q, want %q", got, ExtensionTypeI18N)
	}

	// A pack declared next to a heavier capability still infers the heavier type.
	// Such a manifest is refused at install (validateI18N: a pack gets no sandbox, and
	// nothing else may share its manifest), so what this ordering decides is what a
	// rejected manifest is called in the log — and GetI18N must still read the pack half
	// of it, because refusing an install is not the same as losing the declaration.
	mixed := &Capabilities{LSP: &LSPConfig{}, I18N: &I18NConfig{Locale: "fr", MessagesURL: "fr.json"}}
	if got := inferExtensionType(mixed); got != ExtensionTypeLSP {
		t.Errorf("inferExtensionType(lsp+i18n) = %q, want %q", got, ExtensionTypeLSP)
	}
	if mixed.GetI18N() == nil {
		t.Error("an i18n declaration beside another capability must still be readable as a pack")
	}

	if got := inferExtensionType(&Capabilities{}); got != "" {
		t.Errorf("inferExtensionType(empty) = %q, want empty", got)
	}
}

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

func TestValidate_I18NConfig(t *testing.T) {
	// Absent is the common case — every non-translation extension — and is not an error.
	var none *I18NConfig
	if err := none.Validate(); err != nil {
		t.Errorf("nil config must validate (most extensions declare no pack): %v", err)
	}

	valid := []I18NConfig{
		{Locale: "zh", MessagesURL: "messages.json"},
		{Locale: "pt", MessagesURL: "pt/messages.json"},
		{Locale: "sr", MessagesURL: "catalogs/sr.json", PromptsDir: "prompts"},
		{Locale: "ko", MessagesURL: "messages.json"},
	}
	for _, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("valid shape %+v rejected: %v", c, err)
		}
	}

	bad := []struct {
		name    string
		cfg     I18NConfig
		wantSub string
	}{
		{"no locale", I18NConfig{MessagesURL: "messages.json"}, "locale is required"},
		{"blank locale", I18NConfig{Locale: "   ", MessagesURL: "messages.json"}, "locale is required"},
		{"locale is a path", I18NConfig{Locale: "../../etc", MessagesURL: "m.json"}, "language subtag"},
		// One pack is one language: a region is refused rather than reduced, because
		// "zh-CN" and "zh-TW" are different languages and a pack naming both would decide
		// for the reader which one they meant. validateLocale says so in the error.
		{"locale carries a region", I18NConfig{Locale: "zh-CN", MessagesURL: "m.json"}, "no region"},
		{"locale is uppercase", I18NConfig{Locale: "FR", MessagesURL: "m.json"}, "must be lowercase"},
		{"no catalog", I18NConfig{Locale: "zh"}, "messages_url is required"},
		{"https catalog", I18NConfig{Locale: "zh", MessagesURL: "https://evil.test/m.json"}, "external reference"},
		{"data catalog", I18NConfig{Locale: "zh", MessagesURL: "data:application/json,{}"}, "external reference"},
		{"protocol-relative", I18NConfig{Locale: "zh", MessagesURL: "//evil.test/m.json"}, "external reference"},
		// A leading "/" and a leading "./" were both accepted while the rule was looser;
		// the value is projected into the registry index and resolved by the loader, so
		// there is exactly one spelling of every pack path now.
		{"absolute catalog", I18NConfig{Locale: "zh", MessagesURL: "/messages.json"}, "is absolute"},
		{"dot-slash catalog", I18NConfig{Locale: "zh", MessagesURL: "./messages.json"}, "must not contain"},
		{"traversal", I18NConfig{Locale: "zh", MessagesURL: "../../../etc/passwd"}, "must not contain"},
		{"nested traversal", I18NConfig{Locale: "zh", MessagesURL: "a/../../b.json"}, "must not contain"},
		{"backslash", I18NConfig{Locale: "zh", MessagesURL: `a\b.json`}, `separators`},
		{"control char", I18NConfig{Locale: "zh", MessagesURL: "mess\nages.json"}, "control character"},
		{"over-long catalog", I18NConfig{Locale: "zh", MessagesURL: strings.Repeat("a", 300) + ".json"}, "longer than"},
		{"traversal in prompts_dir", I18NConfig{Locale: "zh", MessagesURL: "m.json", PromptsDir: "../prompts"}, "must not contain"},
		{"external prompts_dir", I18NConfig{Locale: "zh", MessagesURL: "m.json", PromptsDir: "https://evil.test/p"}, "external reference"},
	}
	for _, tc := range bad {
		err := tc.cfg.Validate()
		if err == nil {
			t.Errorf("%s: accepted, want an error naming %q", tc.name, tc.wantSub)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: error %q does not name %q", tc.name, err.Error(), tc.wantSub)
		}
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
