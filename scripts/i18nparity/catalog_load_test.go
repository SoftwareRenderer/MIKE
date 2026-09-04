package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTSCatalog(t *testing.T) {
	// The shape T3 has not chosen yet, so both conventions and the usual TS noise must work.
	path := write(t, t.TempDir(), "en.ts", `// Generated file.
import type { Messages } from './types';

export const en = {
  'app.title': 'MIKE',
  task: {
    delete: {
      title: "Delete task",       // trailing comment
      confirm: 'Delete "{name}"? This cannot be undone.',
    },
  },
  "common.selected": "已选择 {count} 项",
  'long.message': 'first half ' +
    'second half',
  'escaped': 'line\nbreak and \'quote\' and a backslash \\ here',
} satisfies Messages;
`)
	c, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(c.Duplicates) > 0 {
		t.Fatalf("unexpected duplicates %v", c.Duplicates)
	}
	want := map[string]string{
		"app.title":           "MIKE",
		"task.delete.title":   "Delete task",
		"task.delete.confirm": `Delete "{name}"? This cannot be undone.`,
		"common.selected":     "已选择 {count} 项",
		"long.message":        "first half second half",
		"escaped":             "line\nbreak and 'quote' and a backslash \\ here",
	}
	for k, v := range want {
		if c.Values[k] != v {
			t.Errorf("Values[%q] = %q, want %q", k, c.Values[k], v)
		}
	}
	if len(c.Values) != len(want) {
		t.Errorf("got %d keys (%v), want %d", len(c.Values), c.Keys(), len(want))
	}
}

func TestLoadTSCatalogBacktickLiteral(t *testing.T) {
	// A template literal is raw text; `${}` inside it is interpolation, which the placeholder
	// scanner reports separately rather than counting as a message placeholder.
	path := write(t, t.TempDir(), "bt.ts", "export const en = { k: `cost ${n} for {name}` };")
	c, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if c.Values["k"] != "cost ${n} for {name}" {
		t.Errorf("Value = %q", c.Values["k"])
	}
	toks := ExtractPlaceholders(c.Values["k"])
	if len(toks) != 2 || toks[0].Kind != "interpolation" || toks[1].Text != "{name}" {
		t.Errorf("tokens = %+v", toks)
	}
}

func TestLoadTSCatalogFindsDuplicateAndCollidingKeys(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalog(write(t, dir, "dup.ts", `export const en = {
  'a.b': 'one',
  a: { b: 'two' },
  c: 'three',
  c: 'four',
};`))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	got := map[string]bool{}
	for _, d := range c.Duplicates {
		got[d] = true
	}
	if !got["a.b"] {
		t.Errorf("nested a.b and flat 'a.b' flatten to one key and must be reported; duplicates = %v", c.Duplicates)
	}
	if !got["c"] {
		t.Errorf("repeated key c must be reported; duplicates = %v", c.Duplicates)
	}
}

func TestLoadTSCatalogRejectsGarbage(t *testing.T) {
	for name, body := range map[string]string{
		"no object literal": "export const en = computed();",
		"missing colon":     "export const en = { 'a' 'b' };",
		"unterminated":      "export const en = { 'a': 'b' ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadCatalog(write(t, t.TempDir(), "bad.ts", body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadJSONCatalog(t *testing.T) {
	path := write(t, t.TempDir(), "messages.json", `{
  "app.title": "MIKE",
  "task": { "delete": { "title": "删除任务" } },
  "count": 3,
  "list": ["a"]
}`)
	c, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if c.Values["task.delete.title"] != "删除任务" {
		t.Errorf("nested key not flattened: %v", c.Values)
	}
	if len(c.Notes) != 2 {
		t.Errorf("expected a note for the number and the array, got %v", c.Notes)
	}
}

func TestLoadJSONCatalogFindsDuplicateKey(t *testing.T) {
	// encoding/json silently keeps the last duplicate, which is exactly the silent failure here.
	c, err := LoadCatalog(write(t, t.TempDir(), "m.json", `{"a": "一", "a": "二", "b": {"c": "x", "c": "y"}}`))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(c.Duplicates) != 2 || c.Duplicates[0] != "a" || c.Duplicates[1] != "b.c" {
		t.Fatalf("expected [a b.c], got %v", c.Duplicates)
	}
}

func TestLoadJSONCatalogRejectsNonObject(t *testing.T) {
	if _, err := LoadCatalog(write(t, t.TempDir(), "m.json", `["a"]`)); err == nil {
		t.Fatal("expected an error for an array catalogue")
	}
}

func TestLoadCatalogMissingFile(t *testing.T) {
	if _, err := LoadCatalog(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
