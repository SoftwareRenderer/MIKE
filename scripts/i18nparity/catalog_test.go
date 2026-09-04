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
	_, zh := keyDiff(map[string]string{"a": "{count}"}, map[string]string{"a": "{count}"})
	if len(zh) != 0 {
		t.Fatalf("unexpected key diff %v", zh)
	}
	f := &Findings{}
	CompareCatalogs(cat("k", "Selected {count} items"), cat("k", "已选择 {Count} 项"), f, 40)
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
	stats := CompareCatalogs(en, zh, f, 40)
	requireNoErrors(t, f)
	if stats.ENKeys != 4 || stats.ZHKeys != 4 || stats.Compared != 4 {
		t.Fatalf("stats = %+v, want 4/4/4", stats)
	}
}

func TestCompareCatalogsMissingKeyIsAnError(t *testing.T) {
	// The silent-fallback case: the pack still looks Chinese, so nothing else would catch it.
	f := &Findings{}
	CompareCatalogs(cat("a", "One", "b", "Two"), cat("a", "一"), f, 40)
	requireErrorContaining(t, f, "1 key(s) exist in English but not")
	requireErrorContaining(t, f, "\n  b")
}

func TestCompareCatalogsInventedKeyIsAnError(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("a", "One"), cat("a", "一", "a.invented", "二"), f, 40)
	requireErrorContaining(t, f, "invented key")
}

func TestCompareCatalogsPlaceholderCountMatters(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("k", "{a} and {a}"), cat("k", "{a} 和 {a}"), f, 40)
	requireNoErrors(t, f)

	f = &Findings{}
	CompareCatalogs(cat("k", "{a} and {a}"), cat("k", "{a} 和"), f, 40)
	requireErrorContaining(t, f, "lost {a} (en×2, zh×1)")
}

func TestCompareCatalogsGoTemplateSyntaxInMessageIsAnError(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("k", "Hi {name}"), cat("k", "你好 {{name}}"), f, 40)
	requireErrorContaining(t, f, "double-brace")
}

func TestCompareCatalogsUntranslatedIsAWarningOnly(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(
		cat("k", "Delete this task permanently"),
		cat("k", "Delete this task permanently"),
		f, 40)
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
	CompareCatalogs(cat("a", "One"), zh, f, 40)
	requireErrorContaining(t, f, "declares a twice")
}

func TestCompareCatalogsRawKeyAsValue(t *testing.T) {
	f := &Findings{}
	CompareCatalogs(cat("task.delete.title", "Delete task"), cat("task.delete.title", "task.delete.title"), f, 40)
	if !strings.Contains(warnText(f), "raw key") {
		t.Fatalf("expected a raw-key warning, got:\n%s", warnText(f))
	}
}
