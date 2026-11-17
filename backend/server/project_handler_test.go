package server

import (
	"bytes"
	"maps"
	"net/http"
	"path/filepath"
	"testing"
)

func TestProjectCreate(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]any{
		"name": "My Project",
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["name"] != "My Project" {
		t.Fatalf("unexpected name: %s", result["name"])
	}
	if result["id"] == "" || result["id"] == nil {
		t.Fatal("project ID should not be empty")
	}
}

// The name a project is created or renamed with reaches an image ref, a container mount path and a
// worktree directory, so neither door stores one its validator refuses. The character rule itself
// belongs to sandboxruntime.ValidateProjectName (pkg/sandboxruntime/runtime_test.go); what is proven
// here is that both doors call it before anything is written.
func TestProjectNameIsRefusedAtBothDoors(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	projID := createTestProject(t, s)

	invalidNames := []string{
		"-my-project",      // leading dash
		"--verbose",        // double dash
		"-renamed",         // leading dash, the rename door
		"my; rm -rf /",     // shell injection
		"my$(rm /)project", // shell injection
		"my`whoami`proj",   // backtick injection
		"my|cat",           // pipe injection
		"my&project",       // ampersand
		"my!project",       // exclamation
		"my?project",       // question mark
		"my*project",       // asterisk
		"my#project",       // hash
		"my%project",       // percent
		"my^project",       // caret
		`my"project"`,      // double quote
		"my'project",       // single quote
		"my=project",       // equals
		"my+project",       // plus
		"my,project",       // comma
		"my:project",       // colon
		"my;project",       // semicolon
		"my@project",       // at sign
		"my(project)",      // parentheses
		"my<project>",      // angle brackets
		"my\\project",      // backslash
	}

	for _, name := range invalidNames {
		t.Run("create/"+name, func(t *testing.T) {
			req := testRequest("POST", "/api/projects", map[string]any{
				"name": name,
				"path": dir,
			})
			if rr := testResponseAuth(t, s, req); rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for name %q, got %d: %s", name, rr.Code, rr.Body.String())
			}
		})
		t.Run("update/"+name, func(t *testing.T) {
			req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
				"name": name,
			})
			if rr := testResponseAuth(t, s, req); rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for name %q, got %d: %s", name, rr.Code, rr.Body.String())
			}
		})
	}

	// A refused rename leaves the row alone; it is not a rename that failed silently.
	if got := mustJSON[map[string]any](t, bytes.NewReader(getAuth(t, s, "/api/projects/"+projID)))["name"]; got != "test-project" {
		t.Fatalf("the refused renames left the project named %v", got)
	}
}

// An omitted name is derived from the checkout's directory, which is the one thing about the project
// the person did not type — so it is the value a wrong fallback would get wrong.
func TestProjectCreateDerivesTheNameFromThePath(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]any{
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 when name is omitted, got %d: %s", rr.Code, rr.Body.String())
	}
	if want := filepath.Base(dir); mustJSON[map[string]any](t, rr.Body)["name"] != want {
		t.Fatalf("derived name = %v, want %q", mustJSON[map[string]any](t, rr.Body)["name"], want)
	}
}

// sandbox_enabled is half a decision: the network-isolation half has to be spelled, because a task
// either reaches the network or does not, and an omitted key would let the create answer for it.
func TestProjectCreateSandboxEnabledNeedsTheIsolationDecision(t *testing.T) {
	t.Run("with_the_isolation_spelled", func(t *testing.T) {
		s := testServer(t)
		dir := t.TempDir()
		initGitRepo(t, dir)

		req := testRequest("POST", "/api/projects", map[string]any{
			"name":                      "Sandboxed Project",
			"path":                      dir,
			"sandbox_enabled":           true,
			"sandbox_network_isolation": false,
		})
		rr := testResponseAuth(t, s, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
		}

		result := mustJSON[map[string]any](t, rr.Body)
		if result["sandbox_enabled"] != true {
			t.Fatalf("expected sandbox_enabled to be true, got %v", result["sandbox_enabled"])
		}
		if result["sandbox_network_isolation"] == true {
			t.Fatal("expected sandbox_network_isolation to be false")
		}
		// The stored row, not just the create response: every later sandbox start reads this.
		stored := mustJSON[map[string]any](t, bytes.NewReader(getAuth(t, s, "/api/projects/"+result["id"].(string))))
		if stored["sandbox_enabled"] != true {
			t.Fatalf("GET: expected sandbox_enabled to be true, got %v", stored["sandbox_enabled"])
		}
	})

	t.Run("without_the_isolation", func(t *testing.T) {
		s := testServer(t)
		dir := t.TempDir()
		initGitRepo(t, dir)

		req := testRequest("POST", "/api/projects", map[string]any{
			"name":            "Sandboxed Project",
			"path":            dir,
			"sandbox_enabled": true,
		})
		rr := testResponseAuth(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
		if msg, ok := mustJSON[map[string]any](t, rr.Body)["error"].(string); !ok || msg == "" {
			t.Fatal("expected error message in response")
		}

		// The rule is about opting in, so a project that does not opt in needs no second answer.
		req = testRequest("POST", "/api/projects", map[string]any{
			"name":            "Non-Sandboxed Project",
			"path":            dir,
			"sandbox_enabled": false,
		})
		if rr := testResponseAuth(t, s, req); rr.Code != http.StatusCreated {
			t.Fatalf("expected 201 for non-sandboxed project, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestProjectCreateDuplicatePath(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create first project
	req := testRequest("POST", "/api/projects", map[string]any{
		"name": "Project A",
		"path": dir,
	})
	testResponseAuth(t, s, req)

	// Create second project with same path — should fail (500 for UNIQUE constraint violation)
	req = testRequest("POST", "/api/projects", map[string]any{
		"name": "Project B",
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code < http.StatusBadRequest {
		t.Fatalf("expected error for duplicate path, got %d", rr.Code)
	}
}

func TestProjectList(t *testing.T) {
	s := testServer(t)

	// Create 2 projects
	for _, name := range []string{"Project A", "Project B"} {
		dir := t.TempDir()
		initGitRepo(t, dir)
		req := testRequest("POST", "/api/projects", map[string]any{
			"name": name,
			"path": dir,
		})
		testResponseAuth(t, s, req)
	}

	req := testRequest("GET", "/api/projects", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	projects := mustJSON[[]map[string]any](t, rr.Body)
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	// Should be sorted by name
	if projects[0]["name"] != "Project A" || projects[1]["name"] != "Project B" {
		t.Fatalf("unexpected order: %v, %v", projects[0]["name"], projects[1]["name"])
	}
}

func TestProjectGetByIDNotFound(t *testing.T) {
	s := testServer(t)

	req := testRequest("GET", "/api/projects/nonexistent", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestProjectUpdate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"name": "Renamed Project",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["name"] != "Renamed Project" {
		t.Fatalf("unexpected name: %s", result["name"])
	}
}

func TestProjectUpdateSandboxNetworkIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Verify default: sandbox disabled, network isolation disabled
	req := testRequest("GET", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	result := mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] == true {
		t.Fatal("expected sandbox_enabled to be false by default")
	}
	if result["sandbox_network_isolation"] == true {
		t.Fatal("expected sandbox_network_isolation to be false by default")
	}

	// Enable sandbox and network isolation
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled":           true,
		"sandbox_network_isolation": true,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to be true")
	}
	if result["sandbox_network_isolation"] != true {
		t.Fatal("expected sandbox_network_isolation to be true")
	}

	// Disable only network isolation (sandbox stays on)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_network_isolation": false,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to still be true")
	}
	if result["sandbox_network_isolation"] != false {
		t.Fatal("expected sandbox_network_isolation to be false")
	}
}

// What blocks a project's container settings from changing is the work that already owns a container,
// not the rows on its board. DANGER_ZONE is what lets this fixture switch a project to unsandboxed at
// all; the refusal without it is pinned in server_test.go, and the api suite pins the widening refusal.
func TestProjectSandboxChangeBlockedOnlyByRunningWork(t *testing.T) {
	t.Run("backlog_tasks_do_not_block", func(t *testing.T) {
		s := testServer(t)
		projID := createTestProject(t, s)
		createTestTask(t, s, projID, "Task 1")
		createTestTask(t, s, projID, "Task 2")

		for step, body := range []map[string]any{
			{"sandbox_enabled": true, "sandbox_network_isolation": true},
			{"sandbox_enabled": false},
			{"sandbox_enabled": true, "sandbox_network_isolation": true},
		} {
			want := true
			if body["sandbox_enabled"] == false {
				want = false
			}
			rr := testResponseAuth(t, s, testRequest("PUT", "/api/projects/"+projID, body))
			if rr.Code != http.StatusOK {
				t.Fatalf("step %d: expected 200, got %d: %s", step, rr.Code, rr.Body.String())
			}
			if got := mustJSON[map[string]any](t, rr.Body)["sandbox_enabled"]; got != want {
				t.Fatalf("step %d: sandbox_enabled = %v, want %v", step, got, want)
			}
		}
	})

	t.Run("a_running_task_blocks", func(t *testing.T) {
		s := testServer(t)
		projID := createTestProject(t, s)
		taskID := createTestTaskWithLLM(t, s, projID, "Running task")

		rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move",
			map[string]any{"status": "in_progress"}))
		if rr.Code != http.StatusOK {
			t.Fatalf("move to in_progress: expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		rr = testResponseAuth(t, s, testRequest("PUT", "/api/projects/"+projID, map[string]any{
			"sandbox_enabled": true,
		}))
		if rr.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when tasks running, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestProjectCounts(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create tasks in different statuses
	createTestTask(t, s, projID, "Backlog task 1")
	createTestTask(t, s, projID, "Backlog task 2")

	// Move one to in_progress (needs LLM config so auto-start succeeds)
	taskID := createTestTaskWithLLM(t, s, projID, "In progress task")
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{
		"status": "in_progress",
	})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to in_progress failed: %d %s", moveRR.Code, moveRR.Body.String())
	}

	reviewTaskID := createTestTask(t, s, projID, "Review task")
	moveReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+reviewTaskID+"/move", map[string]any{
		"status": "review",
	})
	moveRR2 := testResponseAuth(t, s, moveReq2)
	if moveRR2.Code != http.StatusOK {
		t.Fatalf("move to review failed: %d %s", moveRR2.Code, moveRR2.Body.String())
	}

	// Debug: list tasks and their statuses
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	listRR := testResponseAuth(t, s, listReq)
	tasks := mustJSON[[]map[string]any](t, listRR.Body)
	for _, task := range tasks {
		t.Logf("task %s: status=%s", task["id"], task["status"])
	}

	req := testRequest("GET", "/api/projects/counts", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	counts := mustJSON[map[string]any](t, rr.Body)
	projCounts, ok := counts[projID].(map[string]any)
	if !ok {
		t.Fatalf("project %s not found in counts: %+v", projID, counts)
	}
	if projCounts["backlog"].(float64) != 2 {
		t.Fatalf("expected 2 backlog, got %v", projCounts["backlog"])
	}
	if projCounts["in_progress"].(float64) != 1 {
		t.Fatalf("expected 1 in_progress, got %v", projCounts["in_progress"])
	}
	if projCounts["review"].(float64) != 1 {
		t.Fatalf("expected 1 review, got %v", projCounts["review"])
	}
}

func TestClearDone(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Two Done rows and one that never left the backlog.
	cleared := []string{createTestTask(t, s, projID, "Done task 1"), createTestTask(t, s, projID, "Done task 2")}
	backlogTask := createTestTask(t, s, projID, "Backlog task")
	for _, id := range cleared {
		moveTask(t, s, projID, id, "done")
	}

	// Clear Done takes the whole column and answers what it took.
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := mustJSON[map[string]any](t, rr.Body)["deleted"]; got != float64(len(cleared)) {
		t.Fatalf("deleted = %v, want %d", got, len(cleared))
	}

	// The backlog row is not part of the column, whatever the count said.
	tasks := mustJSON[[]map[string]any](t, testResponseAuth(t, s,
		testRequest("GET", "/api/projects/"+projID+"/tasks", nil)).Body)
	if len(tasks) != 1 || tasks[0]["id"] != backlogTask {
		t.Fatalf("expected only %s after clear done, got %v", backlogTask, tasks)
	}
}

// The memory limit is what the admission gate charges a container for, so it is worth pinning at both
// doors: the default a project gets when it says nothing, the value it asks for, and the two values
// that mean "no limit" — zero and a negative one, which is never stored as a negative.
func TestProjectSandboxMemoryLimit(t *testing.T) {
	newProject := func(t *testing.T, body map[string]any) (*Server, string) {
		t.Helper()
		s := testServer(t)
		dir := t.TempDir()
		initGitRepo(t, dir)
		body = maps.Clone(body)
		body["path"] = dir
		rr := testResponseAuth(t, s, testRequest("POST", "/api/projects", body))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
		}
		return s, mustJSON[map[string]any](t, rr.Body)["id"].(string)
	}

	t.Run("create_stores_the_limit", func(t *testing.T) {
		s, projID := newProject(t, map[string]any{
			"name":                      "Memory Limit Project",
			"sandbox_enabled":           true,
			"sandbox_network_isolation": false,
			"sandbox_memory_limit":      2048,
		})
		stored := mustJSON[map[string]any](t, bytes.NewReader(getAuth(t, s, "/api/projects/"+projID)))
		if stored["sandbox_memory_limit"].(float64) != 2048 {
			t.Fatalf("sandbox_memory_limit = %v, want 2048", stored["sandbox_memory_limit"])
		}
	})

	t.Run("update_moves_the_limit", func(t *testing.T) {
		s, projID := newProject(t, map[string]any{
			"name":                      "Memory Update Project",
			"sandbox_enabled":           true,
			"sandbox_network_isolation": false,
		})
		want := func(step string, sent, expected float64) {
			t.Helper()
			rr := testResponseAuth(t, s, testRequest("PUT", "/api/projects/"+projID, map[string]any{
				"sandbox_memory_limit": sent,
			}))
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d: %s", step, rr.Code, rr.Body.String())
			}
			if got := mustJSON[map[string]any](t, rr.Body)["sandbox_memory_limit"].(float64); got != expected {
				t.Fatalf("%s: sandbox_memory_limit = %v, want %v", step, got, expected)
			}
		}
		// A project that said nothing gets the worker default.
		if got := mustJSON[map[string]any](t, bytes.NewReader(getAuth(t, s, "/api/projects/"+projID)))["sandbox_memory_limit"].(float64); got != 1024 {
			t.Fatalf("default sandbox_memory_limit = %v, want 1024", got)
		}
		want("raise", 4096, 4096)
		want("no limit", 0, 0)
		want("negative clamps to no limit", -512, 0)
	})
}

// Each sandbox list is sent under its sandbox_* request key and read back under the name the row
// stores; clearing one answers an empty list, which is what the settings screen reads as "nothing
// configured". Three fields, one mapping — so one table.
func TestProjectSandboxListSettingsRoundTrip(t *testing.T) {
	rows := []struct {
		name        string
		requestKey  string
		responseKey string
		initial     []map[string]any
		replacement []map[string]any
		checkFirst  func(t *testing.T, first map[string]any)
	}{
		{
			// Container ports only: the host side is auto-assigned.
			name: "forward_ports", requestKey: "sandbox_forward_ports", responseKey: "forward_ports",
			initial:     []map[string]any{{"container_port": 80}, {"container_port": 3000}},
			replacement: []map[string]any{{"container_port": 8080}},
			checkFirst: func(t *testing.T, first map[string]any) {
				if first["container_port"].(float64) != 80 {
					t.Errorf("container_port = %v, want 80", first["container_port"])
				}
			},
		},
		{
			name: "sandbox_mounts", requestKey: "sandbox_mounts", responseKey: "sandbox_mounts",
			initial: []map[string]any{
				{"host_path": "/data/cache", "container_path": "/mnt/cache", "mode": "rw"},
				{"host_path": "/data/config", "container_path": "/mnt/config", "mode": "ro"},
			},
			replacement: []map[string]any{
				{"host_path": "/logs", "container_path": "/var/log/app", "mode": "rw"},
			},
			checkFirst: func(t *testing.T, first map[string]any) {
				for key, want := range map[string]string{
					"host_path": "/data/cache", "container_path": "/mnt/cache", "mode": "rw",
				} {
					if first[key] != want {
						t.Errorf("%s = %v, want %q", key, first[key], want)
					}
				}
			},
		},
		{
			name: "sandbox_envs", requestKey: "sandbox_envs", responseKey: "sandbox_envs",
			initial: []map[string]any{
				{"key": "DATABASE_URL", "value": "postgres://localhost/db"},
				{"key": "API_KEY", "value": "secret123"},
				{"key": "EMPTY_VAR", "value": ""},
			},
			replacement: []map[string]any{{"key": "NEW_VAR", "value": "new_value"}},
			checkFirst: func(t *testing.T, first map[string]any) {
				if first["key"] != "DATABASE_URL" || first["value"] != "postgres://localhost/db" {
					t.Errorf("first env = %v, want DATABASE_URL=postgres://localhost/db", first)
				}
			},
		},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)

			put := func(list []map[string]any) map[string]any {
				t.Helper()
				rr := testResponseAuth(t, s, testRequest("PUT", "/api/projects/"+projID,
					map[string]any{tc.requestKey: list}))
				if rr.Code != http.StatusOK {
					t.Fatalf("put %s: %d %s", tc.requestKey, rr.Code, rr.Body.String())
				}
				return mustJSON[map[string]any](t, rr.Body)
			}

			result := put(tc.initial)
			list, ok := result[tc.responseKey].([]any)
			if !ok {
				t.Fatalf("%s is %T, want an array", tc.responseKey, result[tc.responseKey])
			}
			if len(list) != len(tc.initial) {
				t.Fatalf("%s holds %d rows, want %d", tc.responseKey, len(list), len(tc.initial))
			}
			tc.checkFirst(t, list[0].(map[string]any))

			if list := put(tc.replacement)[tc.responseKey].([]any); len(list) != 1 {
				t.Fatalf("%s holds %d rows after a replacement, want 1", tc.responseKey, len(list))
			}

			// Cleared: the row answers with nothing configured, and the key carries no list of its own.
			if got := put([]map[string]any{})[tc.responseKey]; got != nil {
				if list, isList := got.([]any); !isList || len(list) != 0 {
					t.Fatalf("%s after clearing = %v, want it empty or omitted", tc.responseKey, got)
				}
			}
		})
	}
}
