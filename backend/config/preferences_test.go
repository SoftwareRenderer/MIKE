package config

import (
	"reflect"
	"strings"
	"testing"

	"mike/backend/types"
)

// preferenceFieldsByTag maps each JSON tag of types.Config to its field, and reports which of those
// fields a table entry can hold: a pointer to a scalar, whose nil is the answer "never asked".
// *OperatingMode counts (a defined string); *KeyboardShortcuts does not, though it has a row.
func preferenceFieldsByTag() (byTag map[string]reflect.StructField, preference map[string]bool) {
	byTag = map[string]reflect.StructField{}
	preference = map[string]bool{}
	cfg := reflect.TypeOf(types.Config{})
	for i := range cfg.NumField() {
		f := cfg.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" {
			continue
		}
		byTag[tag] = f
		if f.Type.Kind() != reflect.Pointer {
			continue
		}
		switch f.Type.Elem().Kind() {
		case reflect.Bool, reflect.Int, reflect.String:
			preference[tag] = true
		}
	}
	return byTag, preference
}

// sampleRow is one row each shape reads, and writes back byte for byte.
func sampleRow(p configPref) string {
	switch p.(type) {
	case stringPref:
		return "sample"
	case flagPref:
		return "true"
	case intPref:
		return "7"
	case modePref:
		return string(types.ModePlan)
	case shortcutsPref:
		return `{"start_task":"ctrl+t"}`
	}
	return ""
}

// TestPreferenceTableCoversEveryPreference is the completeness check a round-trip test cannot be.
// updates_test.go walks every preference through the handler, so a mistyped key already fails a test;
// what nothing could see is the preference never plumbed at all — a field added to types.Config and left
// out of the table reads as unset forever, saves nothing, and answers a PUT by dropping the key.
func TestPreferenceTableCoversEveryPreference(t *testing.T) {
	byTag, preference := preferenceFieldsByTag()

	seen := map[string]bool{}
	for _, p := range configPrefs {
		key := p.rowKey()
		if seen[key] {
			t.Errorf("two table entries store row %q", key)
			continue
		}
		seen[key] = true

		field, ok := byTag[key]
		if !ok {
			t.Errorf("table entry %q names no types.Config field by its JSON tag, so the row is stored "+
				"and never carried", key)
			continue
		}

		// decode then encode must move the field the key names and hand back the same row. An entry
		// whose closures were copied from a neighbour stores one field into two rows and reads the
		// other one back forever.
		raw := sampleRow(p)
		if raw == "" {
			t.Errorf("table entry %q has shape %T, which the test has no sample row for", key, p)
			continue
		}
		cfg := &types.Config{}
		p.decode(raw, cfg)
		if moved := reflect.ValueOf(cfg).Elem().FieldByName(field.Name); !fieldHoldsValue(moved) {
			t.Errorf("table entry %q did not set %s, so it reads or writes someone else's field", key, field.Name)
		}
		text, ok, err := p.encode(cfg)
		if err != nil || !ok || text != raw {
			t.Errorf("row %q: decode(%q) then encode gave (%q, %t, %v), want the row back unchanged",
				key, raw, text, ok, err)
		}
	}

	for tag := range preference {
		if !seen[tag] {
			t.Errorf("types.Config has preference %q, but configPrefs has no entry for it: it is never "+
				"read, never saved, and a PUT drops it", tag)
		}
	}
}

func fieldHoldsValue(v reflect.Value) bool {
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Pointer:
		return !v.IsNil()
	case reflect.String:
		return v.String() != ""
	}
	return false
}

// TestEveryPreferenceMergesItsOwnKey covers the third consumer of the table: the PUT merge. Each entry
// must take its own key out of the body and leave the current value alone when the body never sent it.
// That is the partial-update contract read row by row, which no hand-written line names any more.
func TestEveryPreferenceMergesItsOwnKey(t *testing.T) {
	byTag, _ := preferenceFieldsByTag()
	for _, p := range configPrefs {
		key, body := p.rowKey(), sampleBody(p)
		if body == "" {
			t.Errorf("table entry %q has shape %T, which the test has no body value for", key, p)
			continue
		}
		field := byTag[key]

		updates, err := newConfigUpdates([]byte(`{"` + key + `":` + body + `}`))
		if err != nil {
			t.Fatalf("build a body for %q: %v", key, err)
		}
		cfg := &types.Config{}
		p.merge(updates, cfg)
		if updates.err != nil {
			t.Errorf("row %q: merging the value this endpoint sends was recorded as a bad field: %v", key, updates.err)
		}
		if sent := reflect.ValueOf(cfg).Elem().FieldByName(field.Name); !fieldHoldsValue(sent) {
			t.Errorf("row %q: PUT %q did not set %s", key, body, field.Name)
		}

		// A body that never mentions the key keeps the answer the config holds — a stored false must not
		// come back as no opinion, and an unanswered row must not be written as one.
		kept := &types.Config{}
		p.merge(updates, kept)
		before := reflect.ValueOf(kept).Elem().FieldByName(field.Name).Interface()
		absent, err := newConfigUpdates([]byte(`{}`))
		if err != nil {
			t.Fatalf("build an empty body: %v", err)
		}
		p.merge(absent, kept)
		after := reflect.ValueOf(kept).Elem().FieldByName(field.Name).Interface()
		if !reflect.DeepEqual(before, after) {
			t.Errorf("row %q: a body holding nothing changed %s from %v to %v", key, field.Name, before, after)
		}
	}
}

// sampleBody is the JSON value a PUT sends for each shape, beside sampleRow's stored form of it.
func sampleBody(p configPref) string {
	switch p.(type) {
	case stringPref:
		return `"sample"`
	case flagPref:
		return `true`
	case intPref:
		return `7`
	case modePref:
		return `"plan"`
	case shortcutsPref:
		return `{"start_task":"ctrl+t"}`
	}
	return ""
}

// TestConfigRowKeysComeFromTheTable: the read list is the table's key list, so a full read cannot fetch a
// row the write ladder will not write, or miss one it will.
func TestConfigRowKeysComeFromTheTable(t *testing.T) {
	if len(configRowKeys) != len(configPrefs) {
		t.Fatalf("configRowKeys holds %d rows, the table has %d entries", len(configRowKeys), len(configPrefs))
	}
	inTable := map[string]bool{}
	for _, p := range configPrefs {
		inTable[p.rowKey()] = true
	}
	for _, key := range configRowKeys {
		if !inTable[key] {
			t.Errorf("configRowKeys reads row %q, which no table entry owns", key)
		}
		delete(inTable, key)
	}
	for key := range inTable {
		t.Errorf("table entry %q is absent from configRowKeys, so a full read never fetches its row", key)
	}
}
