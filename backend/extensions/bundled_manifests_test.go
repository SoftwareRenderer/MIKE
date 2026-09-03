package extensions

// The bundled language extensions live in the sibling MIKE-extensions checkout and their manifests are
// what every install has to survive: a declaration this package refuses is a registry entry nobody can
// install.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundledManifests(t *testing.T) map[string]*ExtensionManifest {
	t.Helper()

	// This package sits three levels under the repo root (backend/extensions), so the sibling
	// checkout of the extension registry is one more ".." than people first write.
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "MIKE-extensions", "lsp"))
	if err != nil {
		t.Skipf("cannot resolve the MIKE-extensions path: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("MIKE-extensions is not checked out beside this repo (%s): %v", dir, err)
	}

	found := map[string]*ExtensionManifest{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "manifest.json"))
		if err != nil {
			t.Errorf("%s: no readable manifest.json in the registry source: %v", e.Name(), err)
			continue
		}
		var m ExtensionManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Errorf("%s: manifest.json does not parse: %v", e.Name(), err)
			continue
		}
		found[e.Name()] = &m
	}
	if len(found) == 0 {
		t.Skipf("no extension manifests under %s", dir)
	}
	return found
}

func TestBundledLSPManifests_Validate(t *testing.T) {
	manifests := bundledManifests(t)

	withDependencyCache := 0
	for name, m := range manifests {
		// Normalized first, exactly as the install path does: what gets stored is the normalized
		// shape, so that is the shape that has to be legal.
		m.Capabilities.GetLSP().NormalizeFileTypes()
		if err := m.Capabilities.GetLSP().Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if sb := lspSandboxFromManifest(m); sb != nil && sb.Dependency != nil {
			withDependencyCache++
			// A declaration that names a path the server does not read is worse than none: the
			// user grants a host directory, it gets mounted, and the server still answers from
			// the image. The env name is the least that can be checked mechanically here.
			if sb.Dependency.Env == "" || sb.Dependency.ContainerPath == "" {
				t.Errorf("%s: declares dependency_cache with an empty field (%+v)", name, sb.Dependency)
			}
		}
	}

	// The reason this file exists: at least one bundled server must be using the mechanism, or the
	// schema is untested in anger and this test proves nothing.
	if withDependencyCache == 0 {
		t.Error("no bundled extension declares a dependency_cache — the mechanism has no real user")
	}
}

// TestBundledLSPManifests_CanActuallyFail: a health check must be able to fail.
//
// This exists because of a measured mistake: the gopls check was `go list -e -deps ./...`, and `-e`
// tolerates packages it cannot resolve — the check passed with an empty cache and with a full one, so
// it could never report anything. A check that cannot fail is not a gate.
func TestBundledLSPManifests_CanActuallyFail(t *testing.T) {
	for name, m := range bundledManifests(t) {
		sb := lspSandboxFromManifest(m)
		if sb == nil || sb.Environment == nil {
			continue
		}
		for _, arg := range sb.Environment.Check.Args {
			if strings.Contains(arg, "go list") && strings.Contains(arg, " -e ") {
				t.Errorf("%s: its environment check tolerates the very failure it is asked about (`go list -e` exits 0 on a package graph it could not load), so it can never report unhealthy: %q", name, arg)
			}
		}
	}
}

func TestBundledLSPManifests_GateIsActionable(t *testing.T) {
	for name, m := range bundledManifests(t) {
		sb := lspSandboxFromManifest(m)
		if sb == nil || sb.Environment == nil {
			continue
		}
		// The note is the whole user-facing half of the gate: suppressed noise plus no sentence is
		// indistinguishable from "the server found nothing".
		if sb.Environment.Note == "" {
			t.Errorf("%s: declares an environment gate with no note — the user gets silence where the answer is \"fix your project\"", name)
		}
		// A check is a fact the server itself can answer. A command that reaches the network would
		// always fail: the extension sandbox has none.
		for _, a := range append([]string{sb.Environment.Check.Command}, sb.Environment.Check.Args...) {
			if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
				t.Errorf("%s: environment.check contacts a URL from a sandbox with no network: %q", name, a)
			}
		}
	}
}

// TestBundledLSPManifests_DeclareWhatTheGrammarEngineNeeds: `pairs` are what folding, bracket matching
// and bracket pair colorization are all built from, and the two name fields are a language's naming
// convention written down instead of assumed.
//
// A profile missing them still highlights — that is what the engine's defaults are for, and why an
// extension installed before these fields existed is not left behind. The point of asserting it here is
// the other half: a bundled manifest is what every install gets, and a convention nobody wrote down is a
// convention nobody can turn off.
func TestBundledLSPManifests_DeclareWhatTheGrammarEngineNeeds(t *testing.T) {
	declared := 0
	for name, m := range bundledManifests(t) {
		for profile, spec := range m.Capabilities.GetLSP().Syntax {
			where := fmt.Sprintf("%s: capabilities.lsp.syntax.%s", name, profile)
			declared++
			if len(spec.Pairs) == 0 {
				t.Errorf("%s declares no pairs — nothing to fold, match or colour in a language with brackets", where)
			}
			if spec.CapitalizedTypes == nil {
				t.Errorf("%s leaves capitalized_types undeclared — an upper-case name is a type in this language because the engine guessed, not because the extension said so", where)
			}
			if spec.CallAsFunction == nil {
				t.Errorf("%s leaves call_as_function undeclared — a name applied to arguments is a function, and an author who disagrees needs a field to say it in", where)
			}
		}
	}
	if declared == 0 {
		t.Error("no bundled extension declares a syntax profile — the grammar engine has no real user")
	}
}
