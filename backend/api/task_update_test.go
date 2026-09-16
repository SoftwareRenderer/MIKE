package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"mike/backend/types"
)

// TestPUTTask_FieldWrongTypeIsRejectedWithoutPanicOrSilence: a wrong-typed field in a partial task
// update is a 400 and changes nothing. It used to be a 500 (single-value type assertion panics) or
// a 200 that dropped the value and let the UI believe its own save.
func TestPUTTask_FieldWrongTypeIsRejectedWithoutPanicOrSilence(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)
	const path = "/api/projects/task-val-proj/tasks/task-val-task"

	// One row per accepted type, so a new field joins a table that already proves the property.
	cases := []struct {
		name string
		body string
		// check reads the field that must not have moved.
		check func(task *types.Task) string
		want  string
	}{
		{"title is a number", `{"title":123}`, func(tk *types.Task) string { return tk.Title }, "validation task"},
		{"prompt is an object", `{"prompt":{"a":1}}`, func(tk *types.Task) string { return tk.Prompt }, ""},
		{"auto_commit is a string", `{"auto_commit":"yes"}`, func(tk *types.Task) string { return boolText(tk.AutoCommit) }, "false"},
		{"auto_merge is a string", `{"auto_merge":"on"}`, func(tk *types.Task) string { return boolText(tk.AutoMerge) }, "false"},
		{"max turns is a string", `{"max_consecutive_failed_turns":"many"}`, func(tk *types.Task) string { return itoa(tk.MaxConsecutiveFailedTurns) }, "0"},
		{"status is a number", `{"status":2}`, func(tk *types.Task) string { return string(tk.Status) }, string(types.StatusBacklog)},
		{"execution_state is a bool", `{"execution_state":true}`, func(tk *types.Task) string { return string(tk.ExecutionState) }, ""},
		{"operating_mode is a number", `{"operating_mode":1}`, func(tk *types.Task) string { return string(tk.OperatingMode) }, ""},
		// The branch fields reach a git command line, so a wrong type must not slip past the
		// ref validator by way of being dropped.
		{"branch is a number", `{"branch":1}`, func(tk *types.Task) string { return tk.Branch }, "main"},
		// Values of the right type but not a valid member are refused the same way: a status
		// no column renders, an execution_state no transition reads, a mode no tool set
		// dispatches on are all worse for the user than a rejected request.
		{"status unknown", `{"status":"parking_lot"}`, func(tk *types.Task) string { return string(tk.Status) }, string(types.StatusBacklog)},
		{"execution_state unknown", `{"execution_state":"napping"}`, func(tk *types.Task) string { return string(tk.ExecutionState) }, ""},
		{"operating_mode unknown", `{"operating_mode":"autocomplete"}`, func(tk *types.Task) string { return string(tk.OperatingMode) }, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
			task, err := s.GetTask("task-val-proj", "task-val-task")
			if err != nil {
				t.Fatalf("read stored task: %v", err)
			}
			if got := tc.check(task); got != tc.want {
				t.Errorf("body %s was rejected but still changed the row: field = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestPUTTask_PartialUpdateLeavesAbsentFieldsAlone pins the half of the contract that the
// pointer-field rewrite must not disturb: a field the body does not mention keeps its value,
// and a field the body mentions is written - including the values a naive "is it the zero
// value" check would mistake for absence.
func TestPUTTask_PartialUpdateLeavesAbsentFieldsAlone(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)
	const path = "/api/projects/task-val-proj/tasks/task-val-task"

	// Seed a task where every interesting field is non-zero, so "absent" and "zero" differ.
	seed := `{"title":"seeded","prompt":"seed prompt","output":"seed output","session_id":"sess-1",
		"model_name":"model-a","llama_server_url":"http://example/v1","reasoning_effort":"high",
		"max_consecutive_failed_turns":7,"auto_commit":true,"auto_merge":true,
		"subtasks_auto_merge":true,"title_generating":true,"operating_mode":"plan"}`
	if rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(seed)); rr.Code != http.StatusOK {
		t.Fatalf("seed PUT: status %d body %s", rr.Code, rr.Body.String())
	}

	// One field written; every other seeded field must survive it.
	rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(`{"title":"renamed"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("rename PUT: status %d body %s", rr.Code, rr.Body.String())
	}
	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}
	if task.Title != "renamed" {
		t.Errorf("title = %q, want the value the body sent", task.Title)
	}
	for _, f := range []struct {
		name string
		got  string
		want string
	}{
		{"prompt", task.Prompt, "seed prompt"},
		{"output", task.Output, "seed output"},
		{"session_id", task.SessionID, "sess-1"},
		{"model_name", task.ModelName, "model-a"},
		{"llama_server_url", task.LlamaServerURL, "http://example/v1"},
		{"reasoning_effort", task.ReasoningEffort, "high"},
		{"operating_mode", string(task.OperatingMode), "plan"},
	} {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q - a field the body never mentioned must not move", f.name, f.got, f.want)
		}
	}
	if task.MaxConsecutiveFailedTurns != 7 || !task.AutoCommit || !task.AutoMerge || !task.SubtasksAutoMerge || !task.TitleGenerating {
		t.Errorf("a numeric or boolean field was reset by a body that never mentioned it: %+v", task)
	}

	// A present false/0/"" is a write, not an omission. These are the rows a plain struct
	// decode cannot express - an omitted field and a false one look identical once decoded -
	// which is the whole reason the fields are pointers.
	clear := `{"max_consecutive_failed_turns":0,"output":"","prompt":""}`
	if rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(clear)); rr.Code != http.StatusOK {
		t.Fatalf("clear PUT: status %d body %s", rr.Code, rr.Body.String())
	}
	task, err = s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}
	if task.Prompt != "" {
		t.Errorf("prompt = %q, want the explicit empty string the body sent", task.Prompt)
	}
	if task.MaxConsecutiveFailedTurns != 0 {
		t.Errorf("max_consecutive_failed_turns = %d, want the explicit 0 the body sent", task.MaxConsecutiveFailedTurns)
	}
	if task.Output != "" {
		t.Errorf("output = %q, want the explicit empty string the body sent", task.Output)
	}
}

// TestPUTTask_ModeChangeAndPrerequisiteRules covers the two derived behaviours the PUT handler
// owns beyond copying fields: switching operating mode stops a sandbox that is not running so
// the next start remounts with the new mode, and auto_merge implies auto_commit.
func TestPUTTask_ModeChangeAndPrerequisiteRules(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)
	const path = "/api/projects/task-val-proj/tasks/task-val-task"

	// There is no "unset" mode to ask for. Runtime treats an empty mode as act by falling
	// through the tool-set switch, so storing "" would leave a task that behaves like act while
	// every reader sees something else - the client is told instead.
	if rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(`{"operating_mode":""}`)); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty-mode PUT: status %d body %s, want 400", rr.Code, rr.Body.String())
	}
	// A real mode change lands, and the stored row says so.
	if rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(`{"operating_mode":"explore"}`)); rr.Code != http.StatusOK {
		t.Fatalf("explore PUT: status %d body %s", rr.Code, rr.Body.String())
	}
	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}
	if task.OperatingMode != types.ModeExplore {
		t.Fatalf("operating_mode = %q, want explore", task.OperatingMode)
	}

	// auto_merge without auto_commit: the handler sets auto_commit, because auto-commit is a
	// prerequisite for auto-merge and storing the pair the other way round is a task that
	// silently never merges.
	if rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(`{"auto_commit":false,"auto_merge":true}`)); rr.Code != http.StatusOK {
		t.Fatalf("auto-merge PUT: status %d body %s", rr.Code, rr.Body.String())
	}
	task, err = s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}
	if !task.AutoCommit || !task.AutoMerge {
		t.Errorf("auto_commit=%v auto_merge=%v, want both true after asking for auto-merge", task.AutoCommit, task.AutoMerge)
	}
}

// TestPUTTask_MalformedBodyIsRejectedNotIgnored: the handler used to read the body, hand it to
// json.Unmarshal and discard the error, so a truncated or non-object body applied no fields and
// answered 200. The client could not tell a save from a typo.
func TestPUTTask_MalformedBodyIsRejectedNotIgnored(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)
	const path = "/api/projects/task-val-proj/tasks/task-val-task"

	for _, body := range []string{`{"title":`, `[]`, `"just a string"`, `n`} {
		t.Run(body, func(t *testing.T) {
			rr := doTaskRequest(t, mux, http.MethodPut, path, json.RawMessage(body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for body %s", rr.Code, body)
			}
			task, err := s.GetTask("task-val-proj", "task-val-task")
			if err != nil {
				t.Fatalf("read stored task: %v", err)
			}
			if task.Title != "validation task" {
				t.Errorf("title = %q after a rejected body; row moved", task.Title)
			}
		})
	}
}

// TestPOSTTask_FieldWrongTypeIsRejected covers the create path, which decodes into types.Task
// directly and so never had the assertion problem - except for attachments and the legacy
// plan_mode bool, the two values read out of the raw fields, and operating_mode which no path
// validated at all before this.
func TestPOSTTask_FieldWrongTypeIsRejected(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)
	const path = "/api/projects/task-val-proj/tasks"

	for _, tc := range []struct{ name, body string }{
		{"attachments is a number", `{"title":"t","attachments":42}`},
		{"plan_mode is a string", `{"title":"t","plan_mode":"yes"}`},
		{"operating_mode unknown", `{"title":"t","operating_mode":"autocomplete"}`},
		{"operating_mode wrong type", `{"title":"t","operating_mode":9}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doTaskRequest(t, mux, http.MethodPost, path, json.RawMessage(tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
		})
	}

	// And the shape that must still work: a plan task created by the legacy flag.
	rr := doTaskRequest(t, mux, http.MethodPost, path, json.RawMessage(`{"title":"planned","plan_mode":true}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("legacy plan_mode POST: status %d body %s, want 201", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	if created.OperatingMode != types.ModePlan {
		t.Errorf("operating_mode = %q, want plan from plan_mode:true", created.OperatingMode)
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(i int) string { return strconv.Itoa(i) }
