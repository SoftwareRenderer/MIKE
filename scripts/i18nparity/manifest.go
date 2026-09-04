package main

// Locale-pack manifest validation.
//
// The rules come from the deliverable: a locale pack is data, so it declares an i18n capability
// and nothing that runs. Enforcing "no ui, no network, no sandbox, no permissions" here is the
// cheap half of that argument — the expensive half is T4's installer refusing the same shapes,
// and a pack that carries neither is a pack that will be refused at install time with a worse
// message.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Manifest is the subset of an extension manifest an i18n pack must fill in.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Homepage    string `json:"homepage"`

	// Capabilities are the declared capability blocks, kept as raw JSON so an unexpected or
	// forbidden one is still seen instead of being dropped by an unknown-field decode.
	Capabilities map[string]json.RawMessage
	// TopLevelKeys records every key present, so a stray `permissions` is catchable.
	TopLevelKeys map[string]json.RawMessage
}

var (
	// bareLocale is a bare lowercase BCP-47 primary subtag: "zh", not "zh-Hans", not "ZH",
	// not "zh_CN". A region-qualified or cased tag would not match what the browser reports
	// nor what the loader looks up.
	bareLocale = regexp.MustCompile(`^[a-z]{2,3}$`)
	semver     = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	kebab      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// LoadManifest reads and shape-checks a pack manifest, appending findings to f.
func LoadManifest(path string, f *Findings) *Manifest {
	raw, err := os.ReadFile(path)
	if err != nil {
		f.Errf("manifest: %v", err)
		return nil
	}
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		f.Errf("manifest: %s is not valid JSON: %v", path, err)
		return nil
	}
	// The raw key sets are read separately: a struct decode drops every field it does not model,
	// and the fields it would drop (ui, network, sandbox, permissions) are the ones being checked.
	if err := json.Unmarshal(raw, &m.TopLevelKeys); err != nil {
		f.Errf("manifest: %s is not a JSON object: %v", path, err)
		return nil
	}
	if caps, ok := m.TopLevelKeys["capabilities"]; ok {
		if err := json.Unmarshal(caps, &m.Capabilities); err != nil {
			f.Errf("manifest: capabilities is not a JSON object: %v", err)
		}
	}
	m.validate(f)
	return m
}

func (m *Manifest) validate(f *Findings) {
	if !semver.MatchString(m.Version) {
		f.Errf("manifest.version %q must be a full semver (MAJOR.MINOR.PATCH)", m.Version)
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		f.Errf("manifest.display_name is empty — this is what the language picker shows the user")
	}
	if strings.TrimSpace(m.Description) == "" {
		f.Errf("manifest.description is empty")
	} else if bad := firstNonLatinLetter(m.Description); bad != "" {
		// The rule is "written in English", not "written in ASCII": an em dash, a curly quote or a
		// borrowed é are ordinary English typography (this repo's own docs are full of them), while a
		// description in the pack's own language is a real mistake — the registry listing is not
		// localised, so it would render unreadable to everyone else.
		f.Errf("manifest.description must be English (registry listings are not localised); found the non-Latin letter %q", bad)
	}
	if strings.TrimSpace(m.Author) == "" {
		f.Errf("manifest.author is empty")
	}
	if err := validateHTTPURL("homepage", m.Homepage); err != nil {
		f.Errf("manifest: %v", err)
	}

	// A pack is data. Anything that installs code, opens a socket or mounts a host path does not
	// belong to it, and a manifest that asks for one is refused rather than trimmed.
	forbidden := []string{"ui", "network", "sandbox", "lsp", "agent", "llm", "css"}
	for _, key := range forbidden {
		if _, ok := m.Capabilities[key]; ok {
			f.Errf("capabilities.%s must be absent — a locale pack ships messages and prompts, nothing that executes", key)
		}
	}
	if _, ok := m.TopLevelKeys["permissions"]; ok {
		f.Errf("top-level \"permissions\" must be absent — a locale pack requests no permission")
	}
	if _, ok := m.Capabilities["i18n"]; !ok {
		f.Errf("capabilities.i18n is missing (locale, messages_url, prompts_dir)")
		return
	}
}

// DeriveIdentity returns the (type, name) a pack's location gives it, and checks the manifest
// agrees. Neither field belongs in the manifest: scripts/gen-registry.py reads
// `<type>/<name>/manifest.json` and takes both from the path, and no published manifest repeats
// them. A manifest that does state them is not wrong, but it must not contradict its directory,
// because the registry publishes the directory and the manifest would then describe a different
// extension than the one installed.
func (m *Manifest) DeriveIdentity(pack string, f *Findings) (string, string) {
	abs, _ := filepath.Abs(pack)
	dir := filepath.Dir(abs)
	name, extType := filepath.Base(abs), filepath.Base(dir)

	if extType != "i18n" {
		f.Errf(`pack lives at %s/%s, so the registry would publish it as type %q — a locale pack must live under i18n/<locale>`, extType, name, extType)
	}
	if !kebab.MatchString(name) {
		f.Errf("pack directory %q must be kebab-case (lowercase letters, digits and single hyphens) — it becomes the registry name", name)
	}
	if m.Type != "" && m.Type != extType {
		f.Errf(`manifest.type %q contradicts its directory, which makes it type %q — the registry publishes the directory`, m.Type, extType)
	}
	if m.Name != "" && m.Name != name {
		f.Errf("manifest.name %q contradicts its directory, which makes it %q — the registry publishes the directory", m.Name, name)
	}
	return extType, name
}

// I18nCapability is capabilities.i18n as the deliverable specifies it.
type I18nCapability struct {
	Locale      string `json:"locale"`
	MessagesURL string `json:"messages_url"`
	PromptsDir  string `json:"prompts_dir"`
}

// I18n decodes capabilities.i18n and validates its fields.
func (m *Manifest) I18n(f *Findings) *I18nCapability {
	raw, ok := m.Capabilities["i18n"]
	if !ok {
		return nil
	}
	// Strict on unknown fields: a typo'd key ("messages_uri") would otherwise decode to an empty
	// field and the pack would silently ship no messages.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var i18n I18nCapability
	if err := dec.Decode(&i18n); err != nil {
		f.Errf("capabilities.i18n: %v", err)
		return nil
	}
	// Any leftover bytes after one value means the block was not a single object.
	if dec.More() {
		f.Errf("capabilities.i18n: expected a single JSON object")
	}

	if !bareLocale.MatchString(i18n.Locale) {
		f.Errf(`capabilities.i18n.locale %q must be a bare lowercase BCP-47 subtag (e.g. "zh"); a region, case or separator makes it match nothing`, i18n.Locale)
	}
	if err := validateRelativeAsset("messages_url", i18n.MessagesURL); err != nil {
		f.Errf("capabilities.i18n: %v", err)
	} else if filepath.Ext(i18n.MessagesURL) != ".json" {
		f.Errf("capabilities.i18n.messages_url %q must name a .json file", i18n.MessagesURL)
	}
	if err := validateRelativeAsset("prompts_dir", i18n.PromptsDir); err != nil {
		f.Errf("capabilities.i18n: %v", err)
	}
	return &i18n
}

// validateRelativeAsset rejects anything that could leave the pack directory. The pack is
// materialised under extensions/<author>/<name> and deleted with the extension, so an escaping
// asset path is a file the installer would neither ship nor clean up.
func validateRelativeAsset(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if strings.ContainsAny(v, `\`) {
		return fmt.Errorf("%s %q must not contain a backslash", field, v)
	}
	if strings.Contains(v, "?") || strings.Contains(v, "#") {
		return fmt.Errorf("%s %q must be a plain path (no query or fragment)", field, v)
	}
	if strings.HasPrefix(v, "/") {
		return fmt.Errorf("%s %q must be relative to the pack directory, not absolute", field, v)
	}
	if scheme, rest, ok := strings.Cut(v, "://"); ok && len(scheme) <= 8 {
		return fmt.Errorf("%s %q must not name a scheme (%s); it would be fetched, not read from the pack", field, v, rest)
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return fmt.Errorf("%s %q must not escape the pack directory (\"..\" is present)", field, v)
		}
		if seg == "" || seg == "." {
			return fmt.Errorf(`%s %q must be a clean relative path (empty or "." segment)`, field, v)
		}
	}
	return nil
}

// validateHTTPURL checks a manifest URL field.
func validateHTTPURL(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is empty", field)
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("%s %q is not a URL: %w", field, v, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be an http(s) URL", field, v)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q has no host", field, v)
	}
	return nil
}

// firstNonLatinLetter returns the first letter written in a script English is not written in.
// Punctuation, marks and symbols are ignored: an em dash carries no language.
var descriptionScripts = []*unicode.RangeTable{unicode.Latin, unicode.Common, unicode.Inherited, unicode.Greek}

func firstNonLatinLetter(s string) string {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.In(r, descriptionScripts...) {
			return string(r)
		}
	}
	return ""
}

// requiredPackFiles are the files every MIKE extension directory ships (AGENTS.md -> Licensing),
// plus the review document a machine-translated pack owes a human reviewer.
var requiredPackFiles = []string{"LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "REVIEW.md"}

// CheckPackFiles verifies the pack directory holds what it must and references nothing outside
// itself, which is what "self-contained" means once the installer has checked out a pinned commit.
func CheckPackFiles(dir string, f *Findings) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Errf("pack directory: %v", err)
		return
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.Name()] = true
	}
	for _, want := range requiredPackFiles {
		if !present[want] {
			f.Errf("pack is missing %s (every extension directory ships LICENSE, NOTICE and THIRD_PARTY_NOTICES.md; a machine-translated pack also ships REVIEW.md)", want)
		}
	}
	CheckSelfContained(dir, f)
}

// CheckSelfContained resolves every path under the pack and refuses one that lands outside it.
func CheckSelfContained(dir string, f *Findings) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		f.Errf("pack directory %s cannot be resolved: %v", dir, err)
		return
	}
	rootAbs, _ := filepath.Abs(root)

	var names []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && p != dir {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)

	for _, p := range names {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			f.Warnf("%s cannot be resolved: %v", rel(dir, p), err)
			continue
		}
		resolvedAbs, _ := filepath.Abs(resolved)
		if resolvedAbs != rootAbs && !strings.HasPrefix(resolvedAbs, rootAbs+string(filepath.Separator)) {
			f.Errf("%s resolves outside the pack directory to %s — a pack must be self-contained, because it is deleted with the extension", rel(dir, p), resolvedAbs)
		}
	}
}

func rel(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil {
		return r
	}
	return p
}
