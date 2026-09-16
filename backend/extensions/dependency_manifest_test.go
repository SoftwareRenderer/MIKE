package extensions

// Tests for dependency_cache: a manifest saying where it reads resolved dependencies from. Everything
// the core later binds is derived from these few characters, so they are refused at the manifest, not
// stored.

import (
	"encoding/json"
	"strings"
	"testing"

	"mike/backend/types"
)

func lspManifest(sandbox *SandboxConfig) *LSPConfig {
	c := &LSPConfig{FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}}}
	c.Syntax = map[string]SyntaxSpec{}
	if sandbox != nil {
		c.Sandbox = sandbox
	}
	return c
}

func TestValidate_DependencyCache(t *testing.T) {
	// The path a real gopls manifest declares: where the Go toolchain already looks.
	valid := &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("the declared form must validate: %v", err)
	}
	// Absent is the common case and is not an error: most languages keep their dependencies in
	// the checkout, where the project mount already carries them.
	var none *DependencyCache
	if err := none.Validate(); err != nil {
		t.Errorf("no declaration must validate, got %v", err)
	}

	cases := []struct {
		name string
		dep  DependencyCache
		want string
	}{
		{"no container path", DependencyCache{ContainerPath: "  ", Env: "GOMODCACHE"}, "container_path is required"},
		{"relative container path", DependencyCache{ContainerPath: "go/pkg/mod", Env: "GOMODCACHE"}, "not absolute"},
		// "/" is refused by the charset rule (the pattern needs a segment after the slash); which
		// of ValidateContainerPath's rules catches it does not matter, that it is refused does.
		{"container root", DependencyCache{ContainerPath: "/", Env: "GOMODCACHE"}, "is invalid"},
		{"dotdot", DependencyCache{ContainerPath: "/go/../etc", Env: "GOMODCACHE"}, "must not contain '..'"},
		// A colon or comma would be read as part of the -v spec and silently change what is mounted.
		{"colon in path", DependencyCache{ContainerPath: "/go/pkg:ro", Env: "GOMODCACHE"}, "is invalid"},
		{"comma in path", DependencyCache{ContainerPath: "/go/pkg,extra", Env: "GOMODCACHE"}, "is invalid"},
		{"empty env", DependencyCache{ContainerPath: "/go/pkg/mod", Env: ""}, "not a variable name"},
		// The field is a name. Accepting an assignment would make it an unvalidated second `envs`.
		{"env is an assignment", DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE=/tmp/x"}, "not a variable name"},
		{"env starts with a digit", DependencyCache{ContainerPath: "/go/pkg/mod", Env: "2CACHE"}, "not a variable name"},
		{"env has a metacharacter", DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GO$(id)"}, "not a variable name"},
		{"env has a space", DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GO MOD"}, "not a variable name"},
		// The server's own writable state: a read-only mount over it makes the server publish nothing about a
		// file it cannot load, so a broken file looks clean. Only the tmpfs roots count; a subtree like
		// /tmp/modcache leaves the scratch space usable.
		{"container tmpfs", DependencyCache{ContainerPath: "/tmp", Env: "GOMODCACHE"}, "own writable state"},
		{"container cache dir", DependencyCache{ContainerPath: "/root/.cache/go", Env: "GOMODCACHE"}, "own writable state"},
		// A project's own .cache, deep under the mount, is not the server's state.
		{"deep .cache is a location like any other", DependencyCache{ContainerPath: "/mnt/proj/deep/.cache/mod", Env: "GOMODCACHE"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := lspManifest(&SandboxConfig{Dependency: &tc.dep}).Validate()
			if tc.want == "" {
				// The accepted case exists so the refusal above cannot be widened into "refuse
				// everything with the word cache in it".
				if err != nil {
					t.Fatalf("accepted-to-refuse %+#v: %v", tc.dep, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %+#v, want refusal", tc.dep)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "dependency_cache") {
				t.Errorf("error %q does not name the field the user has to fix", err)
			}
		})
	}
}

func TestValidate_ReloadFiles(t *testing.T) {
	// A reload recycles a language server, which is a cold start: a path that matches everything
	// would pay that on every turn of every project.
	for _, files := range [][]string{{"go.mod"}, {"internal/foo/Cargo.toml"}, {"composer.json", "composer.lock"}} {
		if err := lspManifest(&SandboxConfig{ReloadFiles: files}).Validate(); err != nil {
			t.Errorf("%v must validate: %v", files, err)
		}
	}

	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"blank entry", []string{"go.mod", "  "}, "blank"},
		{"absolute", []string{"/etc/passwd"}, "absolute"},
		{"escaping", []string{"../../etc/gitconfig"}, "'..'"},
		{"backslash separator", []string{"src\\main.go"}, "'/'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := lspManifest(&SandboxConfig{ReloadFiles: tc.files}).Validate()
			if err == nil {
				t.Fatalf("accepted %v, want refusal", tc.files)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "reload_files") {
				t.Errorf("error = %q, want it to name reload_files and mention %q", err, tc.want)
			}
		})
	}
}

func TestValidate_EnvironmentGate(t *testing.T) {
	// A gate whose check is empty would be stored, run as nothing, and read as "the workspace
	// resolves" forever — the exact silence this field exists to prevent.
	if err := lspManifest(&SandboxConfig{Environment: &EnvironmentGate{}}).Validate(); err == nil {
		t.Fatal("a gate with no check was accepted")
	} else if !strings.Contains(err.Error(), "environment") {
		t.Errorf("error %q does not name the field", err)
	}
	if err := lspManifest(&SandboxConfig{Environment: &EnvironmentGate{
		Check:         CommandRef{Command: "/bin/sh", Args: []string{"-c", "go list -e -deps ./... >/dev/null"}},
		SuppressCodes: []string{"BrokenImport"},
	}}).Validate(); err != nil {
		t.Errorf("the declared form must validate: %v", err)
	}
	if err := lspManifest(&SandboxConfig{Environment: &EnvironmentGate{
		Check: CommandRef{Command: "/bin/sh"}, SuppressCodes: []string{"  "},
	}}).Validate(); err == nil {
		t.Error("a blank suppress code was accepted")
	}
}

func TestDependencyCache_IgnoresTheRemovedReadOnlyKey(t *testing.T) {
	// `read_only` is gone: this container always reads, and the containers that fill the cache write
	// it from their own side. A manifest or a stored record that still carries the key must decode,
	// validate and behave exactly like one that never mentioned it — the published registry pins point
	// at commits whose manifests have the field, so refusing it would be an install failure for an
	// extension that asks for nothing.
	for _, raw := range []string{
		`{"container_path":"/go/pkg/mod","env":"GOMODCACHE"}`,
		`{"container_path":"/go/pkg/mod","env":"GOMODCACHE","read_only":true}`,
		`{"container_path":"/go/pkg/mod","env":"GOMODCACHE","read_only":false}`,
	} {
		var d DependencyCache
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if err := d.Validate(); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
		// What matters is the assignment podman is finally handed, not what the struct can print:
		// every form of this declaration must reach the container as the same `-e` argument.
		args, err := types.SandboxEnvArgs([]types.SandboxEnv{{Key: d.Env, Value: d.ContainerPath}})
		if err != nil {
			t.Errorf("%s: container env: %v", raw, err)
			continue
		}
		if len(args) != 2 || args[0] != "-e" || args[1] != "GOMODCACHE=/go/pkg/mod" {
			t.Errorf("%s container env = %v, want [-e GOMODCACHE=/go/pkg/mod]", raw, args)
		}
	}

	// And nothing in either type can express a write request any more: what reaches the container is
	// the mount core builds (always ro), and a declaration only reaches it once a human has granted a
	// host directory.
	var none *SandboxConfig
	if none.DependencyGrant() != "" {
		t.Error("an absent configuration must grant no directory")
	}
	declared := &SandboxConfig{Dependency: &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"}}
	if declared.DependencyGrant() != "" {
		t.Error("a declaration nobody granted must bind nothing")
	}
}

func TestSandboxConfigFromManifest_CarriesTheDeclaredFields(t *testing.T) {
	// The runtime acts on these three off the stored record; if install drops them, the feature is
	// silently off for every installed extension.
	declared := &SandboxConfig{
		Dependency:  &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		ReloadFiles: []string{"go.mod"},
		Environment: &EnvironmentGate{Check: CommandRef{Command: "/bin/sh"}},
	}
	got := sandboxConfigFromManifest(declared)
	if got.Dependency == nil || got.Dependency.ContainerPath != "/go/pkg/mod" || got.Dependency.Env != "GOMODCACHE" {
		t.Errorf("dependency_cache not carried: %+v", got.Dependency)
	}
	if len(got.ReloadFiles) != 1 || got.ReloadFiles[0] != "go.mod" {
		t.Errorf("reload_files not carried: %v", got.ReloadFiles)
	}
	if got.Environment == nil {
		t.Error("environment not carried")
	}
}

func TestMergeManifestSandboxAdditions_ReplacesDeclaredFields(t *testing.T) {
	// These are declarations, not user-additive lists. An update that kept merging the old values
	// would keep acting on a manifest that no longer exists — and a manifest that dropped its gate
	// would keep suppressing codes forever.
	stored := SandboxConfig{
		Dependency:  &DependencyCache{ContainerPath: "/old/mod", Env: "OLD"},
		ReloadFiles: []string{"gone.mod"},
		Environment: &EnvironmentGate{Check: CommandRef{Command: "/old-check"}},
	}
	declared := &SandboxConfig{
		Dependency:  &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		ReloadFiles: []string{"go.mod"},
		Environment: &EnvironmentGate{Check: CommandRef{Command: "/bin/sh"}},
	}
	got := mergeManifestSandboxAdditions(stored, declared)
	if got.Dependency.ContainerPath != "/go/pkg/mod" || got.Dependency.Env != "GOMODCACHE" {
		t.Errorf("dependency_cache not replaced: %+v", got.Dependency)
	}
	if len(got.ReloadFiles) != 1 || got.ReloadFiles[0] != "go.mod" {
		t.Errorf("reload_files not replaced: %v", got.ReloadFiles)
	}
	if got.Environment.Check.Command != "/bin/sh" {
		t.Errorf("environment not replaced: %+v", got.Environment)
	}

	// A manifest that declares nothing any more must leave nothing behind.
	emptied := mergeManifestSandboxAdditions(got, &SandboxConfig{})
	if emptied.Dependency != nil || emptied.ReloadFiles != nil || emptied.Environment != nil {
		t.Error("a manifest that dropped these fields left the previous commit's in place")
	}
	// No declared block at all is "nothing changed about the sandbox", not "clear it".
	untouched := mergeManifestSandboxAdditions(got, nil)
	if untouched.Dependency == nil {
		t.Error("a missing declared block cleared the stored declaration")
	}
}
