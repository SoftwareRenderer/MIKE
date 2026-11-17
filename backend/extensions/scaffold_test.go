package extensions

// The authoring bundle stands between a person's sentence and the manifest the agent writes, so it is
// tested as code: every example passes the validator a load runs, and the grammar page names the fields
// the Go types declare.

import (
	"encoding/json/v2"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// templateRoot is the on-disk bundle the binary embeds — `go:embed` is what makes them the same files.
func templateRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "templates", "extension"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Fatalf("the authoring bundle is not where the binary embeds it: %v", err)
	}
	return root
}

// bundleFS is the bundle as the server sees it: the same directory the binary embeds, so a test that
// reads it reads what a create writes.
func bundleFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(templateRoot(t))
}

// TestEveryExampleManifestPassesTheLoadValidator is the bundle's own gate: an example the agent
// copies must be loadable, or the first extension anyone writes inherits a refusal.
func TestEveryExampleManifestPassesTheLoadValidator(t *testing.T) {
	h, _, _ := devHandler(t)
	examples := filepath.Join(templateRoot(t), ".agents", "skills", "extension-authoring", "examples")
	entries, err := os.ReadDir(examples)
	if err != nil {
		t.Fatalf("the bundle ships no examples: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the bundle ships no examples")
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(examples, e.Name())
		manifest, err := readManifest(dir)
		if err != nil {
			t.Errorf("examples/%s: its own manifest does not read: %v", e.Name(), err)
			continue
		}
		if _, err := h.validateLoadedManifest("examples/"+e.Name(), &manifest, dir); err != nil {
			t.Errorf("examples/%s: install would refuse it: %v", e.Name(), err)
		}
	}
}

func TestEveryImageBearingTypeHasAReferencePage(t *testing.T) {
	references := filepath.Join(templateRoot(t), ".agents", "skills", "extension-authoring", "references")
	for _, typ := range []string{"lsp", "agent", "llm"} {
		if _, err := os.Stat(filepath.Join(references, typ+".md")); err != nil {
			t.Errorf("the %s type has no reference page, so the agent has nothing to read about what it costs: %v", typ, err)
		}
	}
	grammar, err := os.ReadFile(filepath.Join(references, "manifest.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(grammar)
	// Every key the manifest types declare is named in the grammar page, and every field the page names
	// exists in the types.
	declared := manifestJSONKeys(reflect.TypeOf(ExtensionManifest{}))
	for _, key := range declared {
		if !strings.Contains(text, "`"+key+"`") {
			t.Errorf("references/manifest.md never names %q, a field the manifest types declare", key)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `") {
			continue
		}
		cell := strings.TrimSpace(line)
		cell = strings.TrimPrefix(cell, "| `")
		key := cell[:strings.Index(cell, "`")]
		if key == "capabilities" || key == "config_schema" {
			continue // named as their own sections, which is where their fields live
		}
		if !strings.Contains(" "+strings.Join(declared, " ")+" ", " "+key+" ") && !capabilityKeys[key] {
			t.Errorf("references/manifest.md names %q, which no manifest type declares", key)
		}
	}
}

var capabilityKeys = map[string]bool{
	"lsp": true, "ui": true, "css": true, "agent": true, "network": true, "llm": true, "i18n": true, "tools": true,
}

// manifestJSONKeys is every JSON name the manifest's own fields declare — one level deep, which is
// the level the grammar page tables.
func manifestJSONKeys(typ reflect.Type) []string {
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			keys = append(keys, name)
		}
	}
	for _, nested := range []reflect.Type{reflect.TypeOf(Capabilities{})} {
		for i := 0; i < nested.NumField(); i++ {
			tag := nested.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name != "" && name != "-" {
				keys = append(keys, name)
			}
		}
	}
	return keys
}

func TestTheShippedAuthoringSkillIsDiscoverable(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(templateRoot(t), ".agents", "skills", "extension-authoring", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The container lists a skill by the name its own frontmatter carries; a mismatch is a skill
	// nobody offers. Mirrors TestShippedPlanFirstSkillIsDiscoverable.
	if !strings.HasPrefix(string(body), "---\nname: extension-authoring\n") {
		t.Error("the authoring skill's frontmatter does not name itself extension-authoring")
	}
}

func TestTheBundleServesNothingFromADotNameTheExtensionNeeds(t *testing.T) {
	// The file server refuses every dot-prefixed segment, so a file the *extension* must serve cannot
	// live under `.agents/`. The bundle's private tree is fine; the repository root is what is served.
	root := templateRoot(t)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() || !strings.HasSuffix(rel, ".json") {
			return nil
		}
		under := filepath.ToSlash(rel)
		if strings.HasPrefix(under, ".agents/") {
			return nil // the authoring bundle is private on purpose
		}
		if !strings.Contains(under, "/") {
			t.Errorf("%s is a manifest-shaped file at the repository root, which no extension has written yet", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScaffoldWritesTheBundleAndRefusesANameTaken(t *testing.T) {
	h, _, root := devHandler(t)
	h.SetAuthoringBundle(bundleFS(t))

	dir, files, err := h.ScaffoldDevRepo("My Cool Ext!")
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if dir != DevRepoDir(root, "my-cool-ext") {
		t.Errorf("scaffolded to %s, want it under the reserved namespace", dir)
	}
	if len(files) == 0 {
		t.Fatal("nothing was written")
	}
	body, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "My Cool Ext!") {
		t.Error("the name never reached the README the person reads")
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents", "skills", "extension-authoring", "SKILL.md")); err != nil {
		t.Errorf("the authoring skill did not reach the project's own skill root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Error("the scaffold wrote a manifest, deciding the type before the agent was asked")
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s was reported as written and is not there: %v", f, err)
		}
	}

	if _, _, err := h.ScaffoldDevRepo("My Cool Ext!"); err == nil {
		t.Error("a second extension with one name overwrote the first")
	}
	// The refusal left the first repository whole.
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Errorf("the refused scaffold damaged the existing one: %v", err)
	}
}

func TestScaffoldWithoutABundleDeclines(t *testing.T) {
	h, _, _ := devHandler(t)
	if _, _, err := h.ScaffoldDevRepo("Anything"); err == nil {
		t.Fatal("a build carrying no bundle created a repository out of nothing")
	}
}

// TestAFailedScaffoldRemovesWhatItWrote is the rollback half of "nothing survives a half-scaffolded
// repo": a bundle that breaks partway through must leave the name free, whatever made it break.
func TestAFailedScaffoldRemovesWhatItWrote(t *testing.T) {
	h, _, root := devHandler(t)
	h.SetAuthoringBundle(brokenBundle{MapFS: fstest.MapFS{
		"README.md": {Data: []byte("# {{NAME}}\n")},
	}})
	if _, _, err := h.ScaffoldDevRepo("Half Written"); err == nil {
		t.Fatal("a bundle that cannot be read scaffolded anyway")
	}
	if exists(DevRepoDir(root, "half-written")) {
		t.Error("the failed scaffold left a directory claiming the name, so the next attempt is refused by its own wreckage")
	}
}

// brokenBundle reads as a complete bundle until it is walked: one of the names it lists cannot be
// opened, which is when a scaffold has already created the directory and written some of it.
type brokenBundle struct{ fstest.MapFS }

func (b brokenBundle) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return b.MapFS.ReadDir(name)
	}
	return append([]fs.DirEntry{phantomEntry{}}, mustReadDir(b.MapFS)...), nil
}

func mustReadDir(m fstest.MapFS) []fs.DirEntry {
	entries, err := m.ReadDir(".")
	if err != nil {
		panic(err)
	}
	return entries
}

// phantomEntry is a directory entry no Open can answer for.
type phantomEntry struct{}

func (phantomEntry) Name() string               { return "unopenable.md" }
func (phantomEntry) IsDir() bool                { return false }
func (phantomEntry) Type() fs.FileMode          { return 0 }
func (phantomEntry) Info() (fs.FileInfo, error) { return phantomInfo{}, nil }

type phantomInfo struct{}

func (phantomInfo) Name() string       { return "unopenable.md" }
func (phantomInfo) Size() int64        { return 3 }
func (phantomInfo) Mode() fs.FileMode  { return 0o644 }
func (phantomInfo) ModTime() time.Time { return time.Time{} }
func (phantomInfo) IsDir() bool        { return false }
func (phantomInfo) Sys() any           { return nil }

func TestScaffoldIntoAWriteProtectedTreeLeavesNothing(t *testing.T) {
	h, _, root := devHandler(t)
	h.SetAuthoringBundle(bundleFS(t))
	if err := os.MkdirAll(DevRoot(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which a read-only directory does not stop")
	}
	if err := os.Chmod(DevRoot(root), 0o500); err != nil {
		t.Skipf("cannot make the namespace read-only here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(DevRoot(root), 0o755) })

	_, _, err := h.ScaffoldDevRepo("Blocked Ext")
	if err == nil {
		t.Fatal("a read-only extensions volume let an extension be created")
	}
	if !strings.Contains(err.Error(), "extensions") && !strings.Contains(err.Error(), "blocked-ext") {
		t.Errorf("the refusal does not name the path the person needs: %v", err)
	}
	if exists(DevRepoDir(root, "blocked-ext")) {
		t.Error("the failed scaffold left a half-written repository claiming the name")
	}
}

func TestBundleJSONIsWellFormed(t *testing.T) {
	root := templateRoot(t)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return walkErr
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("%s is not JSON: %v", rel, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
