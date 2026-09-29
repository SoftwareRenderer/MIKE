package extensions

// These tests cover the host folders an agent extension may see. The only way a host folder gets there is
// the user approving it: a folder list written in the manifest is a suggestion and gives no permission.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// agentExtWithMount is an installed agent as the installer leaves it: its manifest suggests a folder and
// nothing has been approved yet.
func agentExtWithMount(t *testing.T, store *ExtensionStore) *Extension {
	t.Helper()
	ext := &Extension{
		ID:      "pi-team-pi-test",
		Name:    "pi",
		Type:    ExtensionTypeAgent,
		Enabled: true,
		Config:  map[string]any{},
	}
	ext.Manifest.Capabilities = &Capabilities{Agent: &AgentConfig{
		WasmModule: "adapter.wasm",
		Sandbox: &types.AgentSandbox{
			Mounts: []types.SandboxMount{{HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw"}},
			Envs:   []types.SandboxEnv{{Key: "PI_HOME", Value: "/root/.pi"}},
		},
	}}
	if err := store.Create(ext); err != nil {
		t.Fatalf("create extension: %v", err)
	}
	return ext
}

func putAgentMount(t *testing.T, h *ExtensionHandler, id string, mounts any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"mounts": mounts})
	if err != nil {
		t.Fatal(err)
	}
	return sendAgentMount(t, h, id, string(body))
}

func sendAgentMount(t *testing.T, h *ExtensionHandler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+id+"/agent-mount", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleSetAgentMount(rec, req, id)
	return rec
}

func TestValidateAgentMountGrant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, ".pi", "agent")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent has not run yet, so this folder does not exist but the one above it does.
	notYet := filepath.Join(root, ".other", "agent")
	if err := os.MkdirAll(filepath.Dir(notYet), 0o755); err != nil {
		t.Fatal(err)
	}

	accepted := []struct {
		name string
		in   types.SandboxMount
		want types.SandboxMount
	}{
		{
			"existing directory",
			types.SandboxMount{HostPath: existing, ContainerPath: "/root/.pi/agent", Mode: "rw"},
			types.SandboxMount{HostPath: existing, ContainerPath: "/root/.pi/agent", Mode: "rw"},
		},
		{
			"missing leaf, parent present: created on first run",
			types.SandboxMount{HostPath: notYet, ContainerPath: "/root/.other/agent", Mode: "ro"},
			types.SandboxMount{HostPath: notYet, ContainerPath: "/root/.other/agent", Mode: "ro"},
		},
		{
			"the whole tail missing under a directory that exists",
			types.SandboxMount{HostPath: filepath.Join(root, ".never", "run", "once"), ContainerPath: "/root/.never", Mode: "rw"},
			types.SandboxMount{HostPath: filepath.Join(root, ".never", "run", "once"), ContainerPath: "/root/.never", Mode: "rw"},
		},
		{
			"no mode asked for is read-only",
			types.SandboxMount{HostPath: existing, ContainerPath: "/root/.pi/agent"},
			types.SandboxMount{HostPath: existing, ContainerPath: "/root/.pi/agent", Mode: "ro"},
		},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAgentMountGrant(tc.in)
			if err != nil {
				t.Fatalf("refused a directory of the user's own: %v", err)
			}
			if got != tc.want {
				t.Errorf("grant = %+v, want %+v", got, tc.want)
			}
		})
	}

	refused := []struct {
		name string
		in   types.SandboxMount
		want string
	}{
		{"home itself", types.SandboxMount{HostPath: "~", ContainerPath: "/mnt/home", Mode: "ro"}, "is a home directory"},
		{"credential store", types.SandboxMount{HostPath: "~/.ssh", ContainerPath: "/mnt/keys", Mode: "ro"}, "holds credentials"},
		{"system tree", types.SandboxMount{HostPath: "/etc/pi", ContainerPath: "/etc/pi", Mode: "ro"}, "belongs to the machine"},
		{"whole top level", types.SandboxMount{HostPath: "/tank", ContainerPath: "/tank", Mode: "ro"}, "whole top level"},
		{"filesystem root", types.SandboxMount{HostPath: "/", ContainerPath: "/host", Mode: "ro"}, "must not be the host filesystem root"},
		{"relative", types.SandboxMount{HostPath: ".pi/agent", ContainerPath: "/root/.pi/agent", Mode: "ro"}, "not absolute"},
		{"parent escape", types.SandboxMount{HostPath: "/home/user/.pi/../.ssh", ContainerPath: "/mnt/keys", Mode: "ro"}, "must not contain '..'"},
		{"container path rewriting the mode", types.SandboxMount{HostPath: existing, ContainerPath: "/mnt/x:ro", Mode: "ro"}, "is invalid"},
		{"nowhere to create it", types.SandboxMount{HostPath: "/mike-test-no-such-root/agent", ContainerPath: "/mnt/x", Mode: "ro"}, "nowhere to create it"},
		{"not a directory", types.SandboxMount{HostPath: filepath.Join(root, "a-file"), ContainerPath: "/mnt/x", Mode: "ro"}, "is not a directory"},
	}
	if err := os.WriteFile(filepath.Join(root, "a-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := ValidateAgentMountGrant(tc.in); err == nil {
				t.Fatalf("accepted %+v", got)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAgentMountGrant_ExpandsHomeAndStoresTheTarget(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to expand against")
	}
	// The value saved must be a real host path, because podman does not expand a ~.
	got, err := ValidateAgentMountGrant(types.SandboxMount{HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw"})
	if err != nil {
		t.Fatalf("the proposal for the agent's own configuration directory must be grantable: %v", err)
	}
	if !strings.HasPrefix(got.HostPath, filepath.Clean(home)+string(filepath.Separator)) {
		t.Errorf("grant = %q, want it under the home %q", got.HostPath, home)
	}

	// A symlink is saved as the folder it points to. Saving the link itself would let anyone who can move
	// the link widen the approval later.
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	got, err = ValidateAgentMountGrant(types.SandboxMount{HostPath: link, ContainerPath: "/root/.pi/agent", Mode: "ro"})
	if err != nil {
		t.Fatalf("a link to a real directory must be grantable: %v", err)
	}
	if got.HostPath != filepath.Clean(target) {
		t.Errorf("grant = %q, want the resolved %q", got.HostPath, filepath.Clean(target))
	}
}

func TestHandleSetAgentMount_GrantsAndRevokes(t *testing.T) {
	t.Parallel()
	h, store := testHandlerWithStore(t)
	ext := agentExtWithMount(t, store)
	dir := t.TempDir()

	rec := putAgentMount(t, h, ext.ID, []types.SandboxMount{
		{HostPath: dir, ContainerPath: "/root/.pi/agent", Mode: "rw"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("grant = %d %s, want 200", rec.Code, rec.Body)
	}
	stored, err := store.GetByID(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	granted := agentMountGrant(stored)
	if len(granted) != 1 || granted[0].HostPath != filepath.Clean(dir) || granted[0].Mode != "rw" {
		t.Errorf("stored grant = %+v, want the one directory just granted", granted)
	}
	// The extension card shows its state from this response, so the response has to carry the new list.
	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	items, ok := resp.Config[AgentMountConfigKey].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("response config = %v, want the grant reported back", resp.Config)
	}

	rec = putAgentMount(t, h, ext.ID, []types.SandboxMount{})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s, want 200", rec.Code, rec.Body)
	}
	stored, _ = store.GetByID(ext.ID)
	if _, present := stored.Config[AgentMountConfigKey]; present {
		t.Errorf("after revoke the config still carries %q: %v", AgentMountConfigKey, stored.Config)
	}
	if len(agentMountGrant(stored)) != 0 {
		t.Errorf("after revoke the grant still reads %+v", agentMountGrant(stored))
	}
}

func TestHandleSetAgentMount_RefusesWithoutWriting(t *testing.T) {
	t.Parallel()
	h, store := testHandlerWithStore(t)
	ext := agentExtWithMount(t, store)
	dir := t.TempDir()

	// The second folder cannot be approved, so the first one must not be saved either.
	rec := putAgentMount(t, h, ext.ID, []types.SandboxMount{
		{HostPath: dir, ContainerPath: "/root/.pi/agent", Mode: "rw"},
		{HostPath: "~/.ssh", ContainerPath: "/root/.ssh", Mode: "rw"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a refused entry = %d %s, want 400", rec.Code, rec.Body)
	}
	stored, err := store.GetByID(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := stored.Config[AgentMountConfigKey]; present {
		t.Errorf("a refused grant was written anyway: %v", stored.Config[AgentMountConfigKey])
	}

	// An extension that suggests no folder has nothing to approve.
	plain := &Extension{ID: "acme-agent-plain", Name: "plain", Type: ExtensionTypeAgent, Config: map[string]any{}}
	plain.Manifest.Capabilities = &Capabilities{Agent: &AgentConfig{WasmModule: "a.wasm"}}
	if err := store.Create(plain); err != nil {
		t.Fatal(err)
	}
	rec = putAgentMount(t, h, plain.ID, []types.SandboxMount{{HostPath: dir, ContainerPath: "/root/.x", Mode: "ro"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("extension with no declared mount = %d %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "declares no agent sandbox mount") {
		t.Errorf("error %q does not say why there is nothing to grant", rec.Body)
	}
}

// TestHandleUpdate_CannotWriteTheMountGrant: a host folder accepted through the ordinary config form would
// miss the checks above and still reach a task container.
func TestHandleUpdate_CannotWriteTheMountGrant(t *testing.T) {
	t.Parallel()
	h, store := testHandlerWithStore(t)
	ext := agentExtWithMount(t, store)

	body := `{"config":{"sandbox_mount":[{"host_path":"/home/dev/.ssh","container_path":"/mnt/keys","mode":"rw"}]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+ext.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleUpdate(rec, req, ext.ID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("config write of the grant = %d %s, want 400", rec.Code, rec.Body)
	}
	stored, err := store.GetByID(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := stored.Config[AgentMountConfigKey]; present {
		t.Errorf("the config write stored a grant anyway: %v", stored.Config[AgentMountConfigKey])
	}

	// The extension's other settings still save: only this one key is refused.
	req = httptest.NewRequest(http.MethodPut, "/api/extensions/"+ext.ID, strings.NewReader(`{"config":{"model":"deep"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.HandleUpdate(rec, req, ext.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("ordinary config write = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestHandleSetAgentMount_AbsentKeyIsNotARevoke(t *testing.T) {
	t.Parallel()
	h, store := testHandlerWithStore(t)
	ext := agentExtWithMount(t, store)
	dir := t.TempDir()
	if rec := putAgentMount(t, h, ext.ID, []types.SandboxMount{{HostPath: dir, ContainerPath: "/root/.pi/agent", Mode: "rw"}}); rec.Code != http.StatusOK {
		t.Fatalf("setup grant = %d %s", rec.Code, rec.Body)
	}

	// A request that leaves out the key must not answer 200 after deleting a working approval.
	if rec := sendAgentMount(t, h, ext.ID, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("absent mounts key = %d %s, want 400", rec.Code, rec.Body)
	}
	stored, _ := store.GetByID(ext.ID)
	if len(agentMountGrant(stored)) != 1 {
		t.Errorf("the grant was changed by a request that named none: %v", stored.Config)
	}
}

// TestResolvedSandboxManifest_ServesAReadableProposal: the sandbox block is written with {{...}}
// placeholders, so the copy a browser sees has to name the real folder while the saved record keeps the
// placeholder.
func TestResolvedSandboxManifest_ServesAReadableProposal(t *testing.T) {
	t.Parallel()
	manifest := ExtensionManifest{ConfigSchema: &ConfigSchema{Properties: []SchemaProp{{
		Key:     "sandbox_mount",
		Type:    "mount",
		Default: []any{map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"}},
	}}}}
	manifest.Capabilities = &Capabilities{Agent: &AgentConfig{
		WasmModule: "pi.wasm",
		Sandbox: &types.AgentSandbox{Mounts: []types.SandboxMount{{
			HostPath:      "{{sandbox_mount_host_path[]}}",
			ContainerPath: "{{sandbox_mount_container_path[]}}",
			Mode:          "{{sandbox_mount_mode[]}}",
		}}},
	}}
	const storedRef = "{{sandbox_mount_host_path[]}}"

	served := resolvedSandboxManifest(manifest).Capabilities.GetAgent().Sandbox
	want := types.SandboxMount{HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw"}
	if len(served.Mounts) != 1 || served.Mounts[0] != want {
		t.Errorf("served proposal = %+v, want %+v", served.Mounts, want)
	}
	if manifest.Capabilities.GetAgent().Sandbox.Mounts[0].HostPath != storedRef {
		t.Errorf("the stored manifest was rewritten: %q", manifest.Capabilities.GetAgent().Sandbox.Mounts[0].HostPath)
	}

	// A Browse entry for the same extension on disk is shown before it is installed, and it asks the same
	// question about a folder.
	entry := localRegistryEntry(localExtension{Author: "team", Name: "pi", Type: ExtensionTypeAgent, Manifest: manifest})
	if got := entry.Capabilities.GetAgent().Sandbox.Mounts[0].HostPath; got != "~/.pi/agent" {
		t.Errorf("local entry proposal = %q, want the resolved directory", got)
	}
	if manifest.Capabilities.GetAgent().Sandbox.Mounts[0].HostPath != storedRef {
		t.Errorf("the stored manifest was rewritten by the local listing: %q", manifest.Capabilities.GetAgent().Sandbox.Mounts[0].HostPath)
	}
}

// TestGrantShapeWording: a refusal about a folder to mount talks about mounting, not about a dependency
// folder.
func TestGrantShapeWording(t *testing.T) {
	t.Parallel()
	homes := []string{"/home/dev"}
	err := checkGrantShape("/home/dev/.ssh", homes, agentMountKind)
	if err == nil {
		t.Fatal("a credential store was accepted as a mount")
	}
	if !strings.Contains(err.Error(), "holds credentials") {
		t.Errorf("error %q does not name what the directory holds", err)
	}
	if strings.Contains(err.Error(), "depend") {
		t.Errorf("mount refusal borrows the dependency wording: %q", err)
	}
	if err := checkGrantShape("/tank/pi/agent", homes, agentMountKind); err != nil {
		t.Errorf("a named directory of the user's own was refused: %v", err)
	}
}
