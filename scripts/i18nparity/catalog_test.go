package main

import (
	"strings"
	"testing"
)

func cat(pairs ...string) *Catalog {
	c := &Catalog{Values: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		c.Values[pairs[i]] = pairs[i+1]
	}
	return c
}

// counted adds a plural table to a catalog the way the loader does: the flattened `key.form`
// values plus the Tables entry that says which message owns those forms. A table is compared as
// ONE message, so a test written against flattened keys alone would be testing the old rule.
func counted(c *Catalog, key string, forms map[string]string) *Catalog {
	if c.Tables == nil {
		c.Tables = map[string]map[string]string{}
	}
	for form, text := range forms {
		c.Values[key+"."+form] = text
	}
	c.Tables[key] = forms
	return c
}

func errText(f *Findings) string  { return strings.Join(f.Errors, "\n") }
func warnText(f *Findings) string { return strings.Join(f.Warnings, "\n") }

func requireErrorContaining(t *testing.T, f *Findings, want string) {
	t.Helper()
	if !strings.Contains(errText(f), want) {
		t.Fatalf("expected an ERROR containing %q, got errors:\n%s\nwarnings:\n%s", want, errText(f), warnText(f))
	}
}

func requireNoErrors(t *testing.T, f *Findings) {
	t.Helper()
	if len(f.Errors) > 0 {
		t.Fatalf("expected no errors, got:\n%s", errText(f))
	}
}

func TestExtractPlaceholders(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  []string // exact token bytes, in order
		kinds []string
	}{
		{"none", "删除任务", nil, nil},
		{"named", `删除 "{name}"？`, []string{`{name}`}, []string{"named"}},
		{"repeated", "{count} of {count}", []string{"{count}", "{count}"}, []string{"named", "named"}},
		{"positional", "row {0} col {1}", []string{"{0}", "{1}"}, []string{"named", "named"}},
		{"multiple", "{a} {b} {a}", []string{"{a}", "{b}", "{a}"}, []string{"named", "named", "named"}},
		{"empty braces", "x {} y", []string{"{}"}, []string{"empty"}},
		{"space in name", "x { count } y", []string{"{ count }"}, []string{"malformed"}},
		{"double brace", "x {{count}} y", []string{"{{count}}"}, []string{"double-brace"}},
		{"unclosed", "x {count y", []string{"{count y"}, []string{"unclosed"}},
		{"units untouched", "12 tokens · 3s · 90 tok/s", nil, nil},
		{"js interpolation is not a placeholder", "cost ${n}", []string{"${n}"}, []string{"interpolation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractPlaceholders(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d tokens %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i].Text != tc.want[i] {
					t.Errorf("token %d = %q, want %q", i, got[i].Text, tc.want[i])
				}
				if got[i].Kind != tc.kinds[i] {
					t.Errorf("token %q kind = %q, want %q", got[i].Text, got[i].Kind, tc.kinds[i])
				}
			}
		})
	}
}

func TestPlaceholdersAreByteIdenticalNotNormalised(t *testing.T) {
	// A translation that re-cases or renames a token breaks the formatter, which looks the name up
	// literally. That is only caught if the token is compared as written.
	// keyDiff works on key sets — a plural table is one key, not one key per form — so both sides
	// come from a catalog, the way CompareCatalogs builds them.
	_, extra := keyDiff(keySet(cat("a", "{count}").Keys()), keySet(cat("a", "{count}").Keys()))
	if len(extra) != 0 {
		t.Fatalf("unexpected key diff %v", extra)
	}
	f := &Findings{}
	CompareCatalogs(cat("k", "Selected {count} items"), cat("k", "已选择 {Count} 项"), "zh", f, 40)
	requireErrorContaining(t, f, "{count}")
	requireErrorContaining(t, f, "{Count}")
}

func TestCompareCatalogsClean(t *testing.T) {
	en := cat(
		"app.title", "MIKE",
		"task.delete.confirm", `Delete "{name}"? This cannot be undone.`,
		"common.selected", "Selected {count} items",
		"stats.tokens", "{tokens} tokens · {secs}s · {rate} tok/s",
	)
	zh := cat(
		"app.title", "MIKE",
		"task.delete.confirm", `删除 "{name}"？此操作无法撤销。`,
		"common.selected", "已选择 {count} 项",
		"stats.tokens", "{tokens} tokens · {secs}秒 · {rate} tok/s",
	)
	f := &Findings{}
	stats := CompareCatalogs(en, zh, "zh", f, 40)
	requireNoErrors(t, f)
	if stats.ENKeys != 4 || stats.PackKeys != 4 || stats.Compared != 4 {
		t.Fatalf("stats = %+v, want 4/4/4", stats)
	}
	if stats.Locale != "zh" {
		t.Fatalf("stats are not labelled with the locale under check: %+v", stats)
	}
}

func TestCompareCatalogsMissingKeyIsAnError(t *testing.T) {
	// The silent-fallback case: the pack still looks Chinese, so nothing else would catch it.
	f := &Findings{}
	CompareCatalogs(cat("a", "One", "b", "Two"), cat("a", "一"), "zh", f, 40)
	requireErrorContaining(t, f, "1 key(s) exist in English but not")
	requireErrorContaining(t, f, "\n  b")
}

func TestCompareCatalogsInventedKeyIsAnError(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("a", "One"), cat("a", "一", "a.invented", "二"), "zh", f, 40)
	requireErrorContaining(t, f, "invented key")
}

func TestCompareCatalogsPlaceholderCountMatters(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("k", "{a} and {a}"), cat("k", "{a} 和 {a}"), "zh", f, 40)
	requireNoErrors(t, f)

	f = &Findings{}
	CompareCatalogs(cat("k", "{a} and {a}"), cat("k", "{a} 和"), "zh", f, 40)
	requireErrorContaining(t, f, "lost {a} (en×2, zh×1)")
}

func TestPlaceholderFindingNamesTheLocaleUnderCheck(t *testing.T) {
	// A German pack reported as "zh×1" sends the reader to the wrong pack.
	f := &Findings{}
	CompareCatalogs(cat("k", "{a} and {a}"), cat("k", "{a} und"), "de", f, 40)
	requireErrorContaining(t, f, "lost {a} (en×2, de×1)")
}

func TestCompareCatalogsGoTemplateSyntaxInMessageIsAnError(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("k", "Hi {name}"), cat("k", "你好 {{name}}"), "zh", f, 40)
	requireErrorContaining(t, f, "double-brace")
}

func TestCompareCatalogsUntranslatedIsAWarningOnly(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(
		cat("k", "Delete this task permanently"),
		cat("k", "Delete this task permanently"),
		"de", f, 40)
	if len(f.Errors) != 0 {
		t.Fatalf("untranslated prose should warn, not error; got:\n%s", errText(f))
	}
	if !strings.Contains(warnText(f), "byte-identical") {
		t.Fatalf("expected a warning about a copied value, got:\n%s", warnText(f))
	}
}

func TestCompareCatalogsDuplicateKeyIsAnError(t *testing.T) {
	f := &Findings{}
	zh := cat("a", "一")
	zh.Duplicates = []string{"a"}
	CompareCatalogs(cat("a", "One"), zh, "zh", f, 40)
	requireErrorContaining(t, f, "declares a twice")
}

func TestCompareCatalogsRawKeyAsValue(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("task.delete.title", "Delete task"),
		cat("task.delete.title", "task.delete.title"), "zh", f, 40)
	if !strings.Contains(warnText(f), "raw key") {
		t.Fatalf("expected a raw-key warning, got:\n%s", warnText(f))
	}
}

// ── counted messages: the counting is owed, English's forms are not ──────────────────

func TestCompareCatalogsFewerFormsThanEnglishIsNotMissing(t *testing.T) {
	// Turkish answers `other` for every count, so `{other}` alone is the whole message. Demanding
	// `one` too is what made every real pack fail before this rule existed.
	en := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} file", "other": "{count} files",
	})
	tr := counted(cat(), "board.card.fileCount", map[string]string{"other": "{count} dosya"})
	f := &Findings{}
	stats := CompareCatalogs(en, tr, "tr", f, 40)
	requireNoErrors(t, f)
	if stats.Counted != 1 || stats.Collapsed != 1 {
		t.Fatalf("a folded form must be counted, not failed: %+v", stats)
	}
}

func TestCompareCatalogsFoldedFormStillOwesItsPlaceholders(t *testing.T) {
	// Folding is legal; losing {count} while folding is not, and the form-by-form pass cannot see
	// it — this is the only comparison that can.
	en := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} file", "other": "{count} files",
	})
	tr := counted(cat(), "board.card.fileCount", map[string]string{"other": "dosya"})
	f := &Findings{}
	CompareCatalogs(en, tr, "tr", f, 40)
	requireErrorContaining(t, f, "folded form")
	requireErrorContaining(t, f, "{count}")
}

func TestCompareCatalogsMoreFormsThanEnglishAreNotExtra(t *testing.T) {
	// Russian counts what English does not; `few`/`many` are not invented keys.
	en := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} file", "other": "{count} files",
	})
	ru := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} файл", "few": "{count} файла", "many": "{count} файлов", "other": "{count} файла",
	})
	f := &Findings{}
	CompareCatalogs(en, ru, "ru", f, 40)
	requireNoErrors(t, f)
}

func TestCompareCatalogsUnreachableFormIsAnError(t *testing.T) {
	// A `one` in a Turkish pack is a string no runtime path can select: translated, then filed in
	// a drawer. The language's own form set is what makes this measurable.
	en := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} file", "other": "{count} files",
	})
	tr := counted(cat(), "board.card.fileCount", map[string]string{
		"one": "{count} dosya", "other": "{count} dosya",
	})
	f := &Findings{}
	CompareCatalogs(en, tr, "tr", f, 40)
	requireErrorContaining(t, f, "can never be selected")
	requireErrorContaining(t, f, "board.card.fileCount.one")
}

func TestCompareCatalogsCountedMessageNeedsOther(t *testing.T) {
	// `other` is the form the runtime falls back to; without it a counted message can be selected
	// with a count no shipped form answers.
	en := counted(cat(), "chat.model.count", map[string]string{
		"one": "{count} model", "other": "{count} models",
	})
	de := counted(cat(), "chat.model.count", map[string]string{"one": "{count} Modell"})
	f := &Findings{}
	CompareCatalogs(en, de, "de", f, 40)
	requireErrorContaining(t, f, "no \"other\" form")
}

func TestCompareCatalogsShapeChangeIsAnError(t *testing.T) {
	// English plain, pack counted: `t()` would build a table where a string is expected.
	f := &Findings{}
	CompareCatalogs(cat("k", "{count} file"),
		counted(cat(), "k", map[string]string{"other": "{count} Datei"}), "de", f, 40)
	requireErrorContaining(t, f, "changed shape")

	f = &Findings{}
	CompareCatalogs(counted(cat(), "k", map[string]string{"one": "{count} file", "other": "{count} files"}),
		cat("k", "{count} Datei"), "de", f, 40)
	requireErrorContaining(t, f, "changed shape")
}

// ── the keys a pack must NOT ship ───────────────────────────────────────────────────

func TestCompareCatalogsEnglishOnlyKeysAreNotOwed(t *testing.T) {
	// The offer renders only while no pack is installed, so no pack can use these strings.
	en := cat("i18n.offer.title", "Install Spanish?", "app.title", "MIKE")
	pack := cat("app.title", "MIKE")
	f := &Findings{}
	stats := CompareCatalogs(en, pack, "es", f, 40)
	requireNoErrors(t, f)
	if stats.Compared != 1 {
		t.Fatalf("the English-only key must not be compared: %+v", stats)
	}
}

func TestCompareCatalogsCarryingAnEnglishOnlyKeyIsAnError(t *testing.T) {
	// Shipping them is not extra coverage: the key is never looked up, so it is dead weight that
	// every future translation pass will pay for.
	en := cat("i18n.offer.title", "Install Spanish?", "app.title", "MIKE")
	pack := cat("app.title", "MIKE", "i18n.offer.title", "¿Instalar español?")
	f := &Findings{}
	CompareCatalogs(en, pack, "es", f, 40)
	requireErrorContaining(t, f, "stay English by design")
}
