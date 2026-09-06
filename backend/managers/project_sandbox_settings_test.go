package managers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// TestCreateProjectRefusesWideningSandboxSettings is the project side of the rule an
// extension manifest has always met: the settings that go into the container's
// argument list are checked before they are stored, because a task runs an LLM's
// commands inside that container and one of these two fields undoes the boundary it
// is supposed to provide.
func TestCreateProjectRefusesWideningSandboxSettings(t *testing.T) {
	h, _ := testProjectHandler(t)
	isolation := false

	tests := []struct {
		name    string
		caps    []string
		mounts  []types.SandboxMount
		wantSub string
	}{
		{name: "capability that reaches the host kernel", caps: []string{"SYS_ADMIN"}, wantSub: "SYS_ADMIN"},
		{name: "same capability, CAP_ prefix", caps: []string{"cap_dac_override"}, wantSub: "DAC_OVERRIDE"},
		{name: "capability shaped like a flag", caps: []string{"--privileged"}, wantSub: "invalid"},
		{name: "two capabilities in one value", caps: []string{"CHOWN ALL"}, wantSub: "invalid"},
		{name: "empty capability", caps: []string{""}, wantSub: "empty"},
		{name: "the whole host filesystem", mounts: []types.SandboxMount{{HostPath: "/", ContainerPath: "/host", Mode: "rw"}}, wantSub: "host filesystem root"},
		{name: "container path rewriting the mount spec", mounts: []types.SandboxMount{{HostPath: "/tmp/x", ContainerPath: "/workspace:rw"}}, wantSub: "container path"},
		{name: "relative host path", mounts: []types.SandboxMount{{HostPath: "build", ContainerPath: "/build"}}, wantSub: "not absolute"},
		{name: "host path walking up", mounts: []types.SandboxMount{{HostPath: "/srv/../root", ContainerPath: "/root"}}, wantSub: "'..'"},
		{name: "mode that is not ro or rw", mounts: []types.SandboxMount{{HostPath: "/tmp/x", ContainerPath: "/x", Mode: "Z"}}, wantSub: "mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			createBareRepo(t, repo)
			proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
				Path:                    repo,
				Name:                    "widening-" + strings.ReplaceAll(tt.name[:8], " ", "-"),
				SandboxEnabled:          true,
				SandboxNetworkIsolation: &isolation,
				SandboxMounts:           tt.mounts,
				SandboxCapabilities:     tt.caps,
			})
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want %d", code, msg, http.StatusBadRequest)
			}
			if !strings.Contains(msg, tt.wantSub) {
				t.Errorf("message = %q, want it to name %q", msg, tt.wantSub)
			}
			if proj != nil {
				t.Fatal("the project was created despite the refused setting")
			}
		})
	}

	// What the rule is not: the operator picks which host directories a sandbox sees,
	// including ones that hold real secrets, and may ask for a capability that cannot
	// reach past the container. "Mount a directory" is the feature; "mount the host
	// filesystem root" and "hand me CAP_SYS_ADMIN" are not uses of it.
	repo := t.TempDir()
	createBareRepo(t, repo)
	proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
		Path:                    repo,
		Name:                    "widening-accepted",
		SandboxEnabled:          true,
		SandboxNetworkIsolation: &isolation,
		SandboxMounts:           []types.SandboxMount{{HostPath: "/go", ContainerPath: "/go", Mode: "ro"}, {HostPath: "/etc", ContainerPath: "/etc"}},
		SandboxCapabilities:     []string{"NET_RAW"},
	})
	if code != http.StatusCreated {
		t.Fatalf("a legitimate sandbox configuration was refused: %d (%s)", code, msg)
	}
	if len(proj.SandboxMounts) != 2 || proj.SandboxCapabilities[0] != "NET_RAW" {
		t.Fatalf("settings were not stored as given: %+v %+v", proj.SandboxMounts, proj.SandboxCapabilities)
	}
}

// TestUpdateProjectRefusesWideningSandboxSettings: the update path is where an
// existing project could be widened, and validation must run before anything is
// written — and before the handler stops the project's tasks.
func TestUpdateProjectRefusesWideningSandboxSettings(t *testing.T) {
	h, projID := testProjectHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)

	for _, body := range []string{
		`{"sandbox_capabilities":["SYS_ADMIN"]}`,
		`{"sandbox_capabilities":["--privileged"]}`,
		`{"sandbox_mounts":[{"host_path":"/","container_path":"/host","mode":"rw"}]}`,
		`{"sandbox_mounts":[{"host_path":"/tmp/cache","container_path":"/cache:rw"}]}`,
		`{"sandbox_mounts":[{"host_path":"relative","container_path":"/cache"}]}`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: status = %d, want %d (%s)", body, rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		p, err := h.store.GetProject(projID)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.SandboxCapabilities) != 0 || len(p.SandboxMounts) != 0 {
			t.Fatalf("rejected %s was written: caps=%v mounts=%v", body, p.SandboxCapabilities, p.SandboxMounts)
		}
	}

	// A valid pair still updates, so this is a rule and not a disabled field.
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID,
		strings.NewReader(`{"sandbox_capabilities":["CHOWN"],"sandbox_mounts":[{"host_path":"/go","container_path":"/go","mode":"ro"}]}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	p, _ := h.store.GetProject(projID)
	if len(p.SandboxCapabilities) != 1 || p.SandboxCapabilities[0] != "CHOWN" || len(p.SandboxMounts) != 1 {
		t.Fatalf("settings not stored: caps=%v mounts=%+v", p.SandboxCapabilities, p.SandboxMounts)
	}
}

// TestCreateProjectMountHostPathIsAbsoluteOnly pins why the host path must be
// absolute: a relative one is resolved against the server's working directory by
// filepath.Abs at container-creation time, which is nobody's idea of where the
// project's cache should live.
func TestCreateProjectMountHostPathIsAbsoluteOnly(t *testing.T) {
	if err := types.ValidateSandboxMount(types.SandboxMount{HostPath: filepath.Join(t.TempDir(), "x"), ContainerPath: "/x"}); err != nil {
		t.Errorf("an absolute host path was refused: %v", err)
	}
	if err := types.ValidateSandboxMount(types.SandboxMount{HostPath: "x", ContainerPath: "/x"}); err == nil {
		t.Error("a relative host path was accepted")
	}
}
