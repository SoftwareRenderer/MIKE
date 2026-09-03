package extensions

// The bundled language extensions live in the sibling MIKE-extensions checkout, and their
// manifests are what every install of them has to survive: a declaration this package refuses is a
// registry entry nobody can install. That makes "our own manifests validate" a real contract
// between the two repos, and a contract worth a test — the alternative is discovering it in an
// install error on someone else's machine.
//
// Skipped when MIKE-extensions is not checked out beside this repo, which is the case in a
// standalone MIKE checkout and in CI for this repo alone.

import (
	"encoding/json"
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

// TestBundledLSPManifests_CanActuallyFail guards the one property a health check has to have, which
// no runtime can check for it: it must be able to fail.
//
// The reason this is a test is a measured mistake. The gopls check was `go list -e -deps ./...`, and
// `-e` means "tolerate packages it cannot load" — so the check exited 0 in a container with no
// module cache at all, exactly as it did in one with a full cache. Both runs: exit 0. A gate that
// cannot fail does not merely do nothing: it reports the workspace healthy while the server answers
// from an empty dependency root, which is the false-clean state this whole mechanism exists to
// remove. The check is now `GOPROXY=off go list -deps ./...`, measured at exit 0 with the cache
// mounted and exit 1 without it.
//
// This is a rule about our own registry data, which is where knowledge of a specific toolchain's
// flags belongs — never in the core, which must stay language-blind.
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
