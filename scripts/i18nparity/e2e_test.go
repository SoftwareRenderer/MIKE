package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The fixture below is a miniature of the real deliverable: the English catalogue and prompts on
// one side, a locale pack (manifest + messages.json + prompts overlay + the three license files +
// REVIEW.md) on the other, and the generated registry entry that publishes it. Run() is the same
// entry point the CLI calls, so a green test here means the command works, not that a
// reimplementation of its logic works.

var fixtureEN = map[string]string{
	"app.title":              "MIKE",
	"task.delete.confirm":    `Delete "{name}"? This cannot be undone.`,
	"common.selected":        "Selected {count} items",
	"stats.tokens":           "{tokens} tokens · {secs}s · {rate} tok/s",
	"branch.merge.confirm":   "Merge {source} into {target}?",
	"settings.revoke.others": "Sign out all other sessions",
}

var fixtureZH = map[string]string{
	"app.title":              "MIKE",
	"task.delete.confirm":    `删除 "{name}"？此操作无法撤销。`,
	"common.selected":        "已选择 {count} 项",
	"stats.tokens":           "{tokens} tokens · {secs} 秒 · {rate} tok/s",
	"branch.merge.confirm":   "将 {source} 合并到 {target}？",
	"settings.revoke.others": "退出所有其他会话",
}

type fixture struct {
	root, en, enPrompts, pack, registry string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	fx := &fixture{
		root:      root,
		en:        filepath.Join(root, "frontend/src/lib/i18n/en.ts"),
		enPrompts: filepath.Join(root, "prompts"),
		pack:      filepath.Join(root, "MIKE-extensions/i18n/zh"), // dir name must equal the locale
		registry:  filepath.Join(root, "MIKE-extensions/registry.json"),
	}

	write(t, filepath.Dir(fx.en), "en.ts", tsCatalog(fixtureEN))
	writeTmplDir(t, fx.enPrompts, enTmpl)
	writeTmplDir(t, filepath.Join(fx.pack, "prompts"), zhTmpl)

	write(t, fx.pack, "messages.json", jsonCatalog(t, fixtureZH))
	write(t, fx.pack, "manifest.json", `{
  "name": "zh",
  "version": "1.0.0",
  "type": "i18n",
  "display_name": "简体中文",
  "description": "Simplified Chinese translation pack for the MIKE interface and prompts.",
  "author": "MIKE",
  "homepage": "https://github.com/SoftwareRenderer/MIKE-extensions",
  "capabilities": {
    "i18n": { "locale": "zh", "messages_url": "messages.json", "prompts_dir": "prompts" }
  }
}`)
	for _, name := range []string{"LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "REVIEW.md"} {
		write(t, fx.pack, name, name+"\n")
	}
	write(t, filepath.Dir(fx.registry), "registry.json",
		`[{"name":"zh","version":"1.0.0","type":"i18n","subdir":"i18n/zh","repo":"https://example.invalid/x","commit":"0000000000000000000000000000000000000000"}]`)
	return fx
}

func (f *fixture) options() Options {
	return Options{EN: f.en, ENPrompts: f.enPrompts, Pack: f.pack, Registry: f.registry, MaxList: 40}
}

func (f *fixture) run(t *testing.T) (*Findings, CatalogStats, PromptStats) {
	t.Helper()
	find := &Findings{}
	missing, cs, ps := Run(f.options(), find)
	if missing {
		t.Fatalf("fixture inputs reported missing: %v", find.Errors)
	}
	return find, cs, ps
}

func (f *fixture) setMessages(t *testing.T, m map[string]string) {
	t.Helper()
	write(t, f.pack, "messages.json", jsonCatalog(t, m))
}

func (f *fixture) setPrompts(t *testing.T, bodies map[string]string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(f.pack, "prompts")); err != nil {
		t.Fatal(err)
	}
	writeTmplDir(t, filepath.Join(f.pack, "prompts"), bodies)
}

func (f *fixture) setManifest(t *testing.T, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.pack, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	mutate(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.pack, "manifest.json", string(out))
}

func tsCatalog(m map[string]string) string {
	var b strings.Builder
	b.WriteString("export const en = {\n")
	for _, k := range sortedStringKeys(m) {
		b.WriteString("  " + strconv.Quote(k) + ": " + strconv.Quote(m[k]) + ",\n")
	}
	b.WriteString("} satisfies Messages;\n")
	return b.String()
}

func jsonCatalog(t *testing.T, m map[string]string) string {
	t.Helper()
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func writeTmplDir(t *testing.T, dir string, bodies map[string]string) {
	t.Helper()
	for name, body := range bodies {
		write(t, dir, name, body)
	}
}

func TestRunCleanPackPasses(t *testing.T) {
	fx := newFixture(t)
	f, cs, ps := fx.run(t)
	requireNoErrors(t, f)
	if len(f.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", f.Warnings)
	}
	if cs.ENKeys != cs.ZHKeys || cs.Compared != cs.ENKeys {
		t.Fatalf("key counts must be equal: %+v", cs)
	}
	if ps.ENFiles != 7 || ps.ZHFiles != 7 || ps.ZHDefines != 1 {
		t.Fatalf("prompt stats = %+v", ps)
	}
}

func TestRunDeletedKeyIsCaught(t *testing.T) {
	// The exact scenario from the deliverable: a key is deleted and the UI silently falls back.
	fx := newFixture(t)
	broken := map[string]string{}
	for k, v := range fixtureZH {
		if k != "task.delete.confirm" {
			broken[k] = v
		}
	}
	fx.setMessages(t, broken)

	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, "task.delete.confirm")
	requireErrorContaining(t, f, "silently falls back to English")
}

func TestRunInventedKeyIsCaught(t *testing.T) {
	fx := newFixture(t)
	invented := map[string]string{}
	for k, v := range fixtureZH {
		invented[k] = v
	}
	invented["task.does.not.exist"] = "不存在"
	fx.setMessages(t, invented)

	f, cs, _ := fx.run(t)
	requireErrorContaining(t, f, "invented key")
	if cs.ENKeys == cs.ZHKeys {
		t.Fatalf("counts should differ: %+v", cs)
	}
}

func TestRunRenamedPlaceholderIsCaught(t *testing.T) {
	fx := newFixture(t)
	broken := map[string]string{}
	for k, v := range fixtureZH {
		broken[k] = strings.ReplaceAll(v, "{count}", "数量")
	}
	fx.setMessages(t, broken)

	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, "common.selected")
	requireErrorContaining(t, f, "{count}")
}

func TestRunPromptDirectiveDroppedIsCaught(t *testing.T) {
	fx := newFixture(t)
	broken := map[string]string{}
	for k, v := range zhTmpl {
		broken[k] = v
	}
	broken["plan-mode.tmpl"] = "你处于 plan mode。不要修改文件。\n" // the define wrapper is gone
	fx.setPrompts(t, broken)

	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, `lost define "plan-mode"`)
}

func TestRunEnglishContractChangeMakesCheckerComplain(t *testing.T) {
	// A template added to the English set must not be silently skipped by the overlay check.
	fx := newFixture(t)
	if err := os.Remove(filepath.Join(fx.enPrompts, "summarize_user.tmpl")); err != nil {
		t.Fatal(err)
	}
	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, "out of date")
}

func TestRunMissingLicenseFilesAreErrors(t *testing.T) {
	fx := newFixture(t)
	for _, name := range []string{"LICENSE", "NOTICE"} {
		if err := os.Remove(filepath.Join(fx.pack, name)); err != nil {
			t.Fatal(err)
		}
	}
	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, "pack is missing LICENSE")
	requireErrorContaining(t, f, "pack is missing NOTICE")
}

func TestRunRegistryEntryRequired(t *testing.T) {
	fx := newFixture(t)
	write(t, filepath.Dir(fx.registry), "registry.json", `[{"name":"clangd","type":"lsp","version":"0.1.0","subdir":"lsp/clangd"}]`)
	f, _, _ := fx.run(t)
	requireErrorContaining(t, f, "no i18n/zh entry")
	requireErrorContaining(t, f, "gen-registry.py")
}

func TestRunRegistryVersionDriftWarns(t *testing.T) {
	fx := newFixture(t)
	fx.setManifest(t, func(m map[string]any) { m["version"] = "1.1.0" })
	f, _, _ := fx.run(t)
	if len(f.Errors) != 0 {
		t.Fatalf("version drift should warn, not error: %v", f.Errors)
	}
	if !strings.Contains(warnText(f), "rerun scripts/gen-registry.py") {
		t.Fatalf("expected a registry drift warning, got %v", f.Warnings)
	}
}

func TestRunLocaleMustMatchDirectoryName(t *testing.T) {
	fx := newFixture(t)
	fx.setManifest(t, func(m map[string]any) {
		caps := m["capabilities"].(map[string]any)
		caps["i18n"].(map[string]any)["locale"] = "zho"
	})
	f, _, _ := fx.run(t)
	if !strings.Contains(warnText(f), "differs from the pack directory name") {
		t.Fatalf("expected a locale/directory warning, got %v", f.Warnings)
	}
}

func TestRunMissingInputsIsNotAFinding(t *testing.T) {
	// "The contract has not landed" must be a third answer, distinct from "this pack is wrong".
	f := &Findings{}
	missing, _, _ := Run(Options{
		EN:        filepath.Join(t.TempDir(), "en.ts"),
		ENPrompts: filepath.Join(t.TempDir(), "prompts"),
		Pack:      filepath.Join(t.TempDir(), "zh"),
		MaxList:   40,
	}, f)
	if !missing {
		t.Fatal("expected inputsMissing")
	}
	if len(f.Errors) != 3 {
		t.Fatalf("expected one error per missing input, got %v", f.Errors)
	}
}

func TestRunManifestAssetPathsLocateTheFiles(t *testing.T) {
	// messages_url / prompts_dir are what the loader resolves; the checker must follow them too,
	// not assume the filenames by convention.
	fx := newFixture(t)
	write(t, filepath.Join(fx.pack, "locale"), "strings.json", jsonCatalog(t, fixtureZH))
	writeTmplDir(t, filepath.Join(fx.pack, "tpl"), zhTmpl)
	fx.setManifest(t, func(m map[string]any) {
		i18n := m["capabilities"].(map[string]any)["i18n"].(map[string]any)
		i18n["messages_url"] = "locale/strings.json"
		i18n["prompts_dir"] = "tpl"
	})
	if err := os.Remove(filepath.Join(fx.pack, "messages.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(fx.pack, "prompts")); err != nil {
		t.Fatal(err)
	}
	f, cs, ps := fx.run(t)
	requireNoErrors(t, f)
	if cs.Compared != len(fixtureEN) || ps.ZHFiles != 7 {
		t.Fatalf("manifest asset paths not followed: %+v %+v", cs, ps)
	}
}
