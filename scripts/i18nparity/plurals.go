package main

// Plural form sets, and what a counted message owes which language.
//
// English counts `{one, other}`. A pack mirrors the counting, not English's forms, because the forms
// are what the language has: `Intl.PluralRules` answers `other` for Turkish always, and
// `one`/`few`/`many` for Russian depending on the number. So the Go validator beside the manifest
// rules requires `other` and calls it sufficient — a table missing
// the selected category degrades to it, so a language with two forms shipping `other` alone is
// complete, not lazy.
//
// Hence the two rules enforced here:
//
//  1. `other` must be there — the only form guaranteed to be selectable;
//  2. a form the language never answers with is dead text — a `one` in a Turkish pack is a string no
//     runtime path can reach.
//
// A form English has that the pack folds into `other` is neither: it is the correct shape for a
// language with fewer forms, and it is counted and reported, never failed.

// pluralCategories are the forms `Intl.PluralRules` can answer with. The runtime selects one of
// these (`I18nRuntime.render`) and the installer accepts only these, so a key outside the set is
// a typo that would never be read.
var pluralCategories = map[string]bool{
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

// pluralFormsByLocale lists the forms each language answers with (CLDR). It is a set of what is
// *reachable*, not a checklist of what to ship: shipping a subset is legal, shipping a form not
// listed here is dead text. A locale absent from the map is not a failure — the checker says it
// had no opinion rather than inventing one.
var pluralFormsByLocale = map[string][]string{
	"de": {"one", "other"},
	"en": {"one", "other"},
	"es": {"one", "many", "other"},
	"fr": {"one", "many", "other"},
	"it": {"one", "many", "other"},
	"ja": {"other"},
	"ko": {"other"},
	"nl": {"one", "other"},
	"pl": {"one", "two", "few", "many", "other"},
	"pt": {"one", "many", "other"},
	"ru": {"one", "few", "many", "other"},
	"tr": {"other"},
	"zh": {"other"},
}

// reachableForms reports the forms `locale` can answer with, and whether the checker knows the
// language at all.
func reachableForms(locale string) (map[string]bool, bool) {
	forms, ok := pluralFormsByLocale[locale]
	if !ok {
		return nil, false
	}
	set := make(map[string]bool, len(forms))
	for _, form := range forms {
		set[form] = true
	}
	return set, true
}

// isPluralTable decides whether the members of an object value are a counted message rather than
// a nested namespace. Both en.ts conventions are legal (flat dotted keys and nested objects), so
// shape is the only honest test: every member named a plural category, and every member a string.
// `{other: '…'}` alone is a table — for zh, ja, ko and tr it is the whole message.
func isPluralTable(members map[string]string, sawNonString bool) bool {
	if sawNonString || len(members) == 0 {
		return false
	}
	for name := range members {
		if !pluralCategories[name] {
			return false
		}
	}
	return true
}
