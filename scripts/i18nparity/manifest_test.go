package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func goodManifestMap() map[string]any {
	return map[string]any{
		"name":         "zh",
		"version":      "1.0.0",
		"type":         "i18n",
		"display_name": "简体中文",
		"description":  "Simplified Chinese translation pack for the MIKE interface and prompts.",
		"author":       "MIKE",
		"homepage":     "https://github.com/SoftwareRenderer/MIKE-extensions",
		"capabilities": map[string]any{
			"i18n": map[string]any{
				"locale":       "zh",
				"messages_url": "messages.json",
				"prompts_dir":  "prompts",
			},
		},
	}
}

func loadManifestOf(t *testing.T, mutate func(map[string]any)) (*Findings, *Manifest) {
	t.Helper()
	m := goodManifestMap()
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := write(t, t.TempDir(), "manifest.json", string(raw))
	f := &Findings{}
	return f, LoadManifest(path, f)
}

func TestManifestGood(t *testing.T) {
	f, m := loadManifestOf(t, nil)
	requireNoErrors(t, f)
	if m == nil {
		t.Fatal("manifest not loaded")
	}
	i18n := m.I18n(f)
	requireNoErrors(t, f)
	if i18n == nil || i18n.Locale != "zh" || i18n.MessagesURL != "messages.json" || i18n.PromptsDir != "prompts" {
		t.Fatalf("i18n capability = %+v", i18n)
	}
}

func TestManifestLocale(t *testing.T) {
	cases := map[string]bool{ // locale -> want accepted
		"zh":         true,
		"en":         true,
		"fra":        true,
		"zh-Hans":    false, // a region or script subtag is not a bare subtag
		"ZH":         false,
		"zh_CN":      false,
		"zh-Hans-CN": false,
		"":           false,
		"zh-Hans_CN": false,
		"toolong":    false,
	}
	for locale, wantOK := range cases {
		t.Run(locale, func(t *testing.T) {
			f, m := loadManifestOf(t, func(m map[string]any) {
				caps := m["capabilities"].(map[string]any)
				i18n := caps["i18n"].(map[string]any)
				i18n["locale"] = locale
			})
			m.I18n(f)
			ok := len(f.Errors) == 0
			if ok != wantOK {
				t.Fatalf("locale %q accepted=%v, want %v; errors:\n%s", locale, ok, wantOK, errText(f))
			}
		})
	}
}

func TestManifestRejectsExecutableCapabilities(t *testing.T) {
	// A locale pack ships data. Anything that runs code, opens a socket or mounts a host path is
	// refused here rather than trimmed, so the pack cannot quietly gain a capability.
	for _, key := range []string{"ui", "network", "sandbox", "lsp", "agent", "llm", "css"} {
		t.Run(key, func(t *testing.T) {
			f, _ := loadManifestOf(t, func(m map[string]any) {
				m["capabilities"].(map[string]any)[key] = map[string]any{}
			})
			requireErrorContaining(t, f, "capabilities."+key+" must be absent")
		})
	}
}

func TestManifestRejectsPermissions(t *testing.T) {
	f, _ := loadManifestOf(t, func(m map[string]any) { m["permissions"] = []string{"fs"} })
	requireErrorContaining(t, f, "permissions")
}

func TestManifestDescriptionMustBeEnglish(t *testing.T) {
	f, _ := loadManifestOf(t, func(m map[string]any) {
		m["description"] = "简体中文翻译包，用于 MIKE 界面与提示词。"
	})
	requireErrorContaining(t, f, "must be English")
}

// The rule is "written in English", not "written in ASCII". This exact false positive fired on
// the sibling ja pack's real description, which is fluent English containing an em dash.
func TestManifestDescriptionAllowsEnglishTypography(t *testing.T) {
	for _, ok := range []string{
		"Data only — no UI injection, no network access, no permissions.",
		"Simplified Chinese pack: “interface strings” and the agent’s prompts.",
		"Résumé-aware caching for café-localised builds, Δt bounded.",
	} {
		t.Run(ok, func(t *testing.T) {
			f, _ := loadManifestOf(t, func(m map[string]any) { m["description"] = ok })
			requireNoErrors(t, f)
		})
	}
	for _, bad := range []string{"简体中文翻译包。", "日本語翻訳パック", "Перевод интерфейса"} {
		t.Run(bad, func(t *testing.T) {
			f, _ := loadManifestOf(t, func(m map[string]any) { m["description"] = bad })
			requireErrorContaining(t, f, "must be English")
		})
	}
}

func TestManifestOtherFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr string
	}{
		{"version not semver", func(m map[string]any) { m["version"] = "1.0" }, "full semver"},
		{"empty display name", func(m map[string]any) { m["display_name"] = " " }, "display_name"},
		{"no homepage", func(m map[string]any) { m["homepage"] = "" }, "homepage is empty"},
		{"ftp homepage", func(m map[string]any) { m["homepage"] = "ftp://example.com" }, "http(s)"},
		{"missing i18n capability", func(m map[string]any) { delete(m["capabilities"].(map[string]any), "i18n") }, "capabilities.i18n is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := loadManifestOf(t, tc.mutate)
			requireErrorContaining(t, f, tc.wantErr)
		})
	}
}

// The registry derives type and name from `<type>/<name>/manifest.json`, and no published
// manifest repeats them. A checker that required them would reject every correct pack.
func TestManifestIdentityIsTheDirectory(t *testing.T) {
	packOf := func(t *testing.T, rel string) string {
		t.Helper()
		return filepath.Join(t.TempDir(), "MIKE-extensions", rel)
	}

	t.Run("fields absent is correct", func(t *testing.T) {
		f, m := loadManifestOf(t, func(m map[string]any) { delete(m, "name"); delete(m, "type") })
		requireNoErrors(t, f)
		extType, name := m.DeriveIdentity(packOf(t, "i18n/zh"), f)
		requireNoErrors(t, f)
		if extType != "i18n" || name != "zh" {
			t.Fatalf("derived (%q,%q), want (i18n,zh)", extType, name)
		}
	})

	t.Run("fields must not contradict the directory", func(t *testing.T) {
		f, m := loadManifestOf(t, func(m map[string]any) { m["name"] = "chinese"; m["type"] = "ui" })
		m.DeriveIdentity(packOf(t, "i18n/zh"), f)
		requireErrorContaining(t, f, "manifest.name")
		requireErrorContaining(t, f, "manifest.type")
	})

	t.Run("must live under i18n", func(t *testing.T) {
		f, m := loadManifestOf(t, nil)
		m.DeriveIdentity(packOf(t, "lsp/zh"), f)
		requireErrorContaining(t, f, "must live under i18n/<locale>")
	})

	t.Run("directory name becomes the registry name", func(t *testing.T) {
		f, m := loadManifestOf(t, nil)
		m.DeriveIdentity(packOf(t, "i18n/zh_CN"), f)
		requireErrorContaining(t, f, "kebab-case")
	})
}

func TestManifestUnknownFieldInI18nIsRefused(t *testing.T) {
	// "messages_uri" would otherwise decode to an empty MessagesURL and the pack would ship no
	// messages while still looking installed.
	raw := `{"name":"zh","version":"1.0.0","type":"i18n","display_name":"简体中文",
	 "description":"English words.","author":"MIKE","homepage":"https://example.com",
	 "capabilities":{"i18n":{"locale":"zh","messages_uri":"messages.json","prompts_dir":"prompts"}}}`
	path := write(t, t.TempDir(), "manifest.json", raw)
	f := &Findings{}
	m := LoadManifest(path, f)
	m.I18n(f)
	requireErrorContaining(t, f, "messages_uri")
}

func TestValidateRelativeAsset(t *testing.T) {
	good := []string{"messages.json", "prompts", "assets/messages.json"}
	bad := []string{"", "/etc/passwd", "../en.ts", "./", "a/../b", "http://evil/x.json", "file:///x", "messages.json?x=1", `a\b`}
	for _, g := range good {
		if err := validateRelativeAsset("f", g); err != nil {
			t.Errorf("%q rejected: %v", g, err)
		}
	}
	for _, b := range bad {
		if err := validateRelativeAsset("f", b); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
}

func TestCheckPackFilesRequiresLicenseTrio(t *testing.T) {
	dir := t.TempDir()
	f := &Findings{}
	CheckPackFiles(dir, f)
	for _, want := range requiredPackFiles {
		if !contains(f.Errors, missingFileMessage(want)) {
			t.Errorf("expected a finding for missing %s; got:\n%s", want, errText(f))
		}
	}
}

func missingFileMessage(name string) string {
	return "pack is missing " + name + " (every extension directory ships LICENSE, NOTICE and THIRD_PARTY_NOTICES.md; a machine-translated pack also ships REVIEW.md)"
}

func TestCheckSelfContainedRefusesSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	for _, name := range requiredPackFiles {
		write(t, dir, name, "x")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f := &Findings{}
	CheckSelfContained(dir, f)
	requireErrorContaining(t, f, "resolves outside the pack directory")
}

func TestCheckSelfContainedAllowsInternalSymlink(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, inner, "base.tmpl", "x")
	if err := os.Symlink(filepath.Join(inner, "base.tmpl"), filepath.Join(dir, "link.tmpl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f := &Findings{}
	CheckSelfContained(dir, f)
	requireNoErrors(t, f)
}
