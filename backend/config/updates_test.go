package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/types"
)

// putConfigRaw sends one raw JSON body straight through the PUT handler and hands back the
// recorder, because these tests assert on the status and the message as well as the outcome.
// (putConfig in config_test.go drives a sequence and fails on any non-200, which is the wrong
// helper when a 400 is the thing under test.)
func putConfigRaw(t *testing.T, cs *ConfigStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	cs.HandleConfig(w, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
	return w
}

// TestPUTConfig_FieldWrongTypeIsRejectedWithoutPanicOrSilence: the config half of the same defect.
// Four fields panicked on a wrong type and the rest were dropped with a 200; now every field answers
// 400 and stores nothing.
func TestPUTConfig_FieldWrongTypeIsRejectedWithoutPanicOrSilence(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// The four that panicked.
		{"worker_provider is a number", `{"worker_provider":5}`},
		{"worker_model is an object", `{"worker_model":{"a":1}}`},
		{"utility_provider is a bool", `{"utility_provider":true}`},
		{"utility_model is an array", `{"utility_model":[]}`},
		// The ones that were dropped without a word.
		{"default_auto_commit is a string", `{"default_auto_commit":"on"}`},
		{"default_auto_merge is a string", `{"default_auto_merge":"yes"}`},
		{"default_subtasks_auto_merge is a string", `{"default_subtasks_auto_merge":"1"}`},
		{"hide_model_badge is a number", `{"hide_model_badge":1}`},
		{"run_siblings_parallel is a string", `{"run_siblings_parallel":"x"}`},
		{"sort_oldest_first is a string", `{"sort_oldest_first":"x"}`},
		{"auto_maximize_chat_mobile is a string", `{"auto_maximize_chat_mobile":"x"}`},
		{"reclaim_idle_sandbox is a string", `{"reclaim_idle_sandbox":"yes"}`},
		{"default_max_failed_turns is a string", `{"default_max_failed_turns":"many"}`},
		{"default_auto_summarize_threshold is a string", `{"default_auto_summarize_threshold":"half"}`},
		// A default nothing dispatches on would be echoed back by the UI and ignored, so it is
		// refused on the way in even though reading one back is coerced (see GetConfig).
		{"default_operating_mode unknown", `{"default_operating_mode":"autocomplete"}`},
		{"default_operating_mode wrong type", `{"default_operating_mode":3}`},
		// Shape errors the old code swallowed by re-encoding through json.Marshal.
		{"providers is a string", `{"providers":"ollama"}`},
		{"keyboard_shortcuts is a string", `{"keyboard_shortcuts":"ctrl+k"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := testConfigStoreWithStore(t)
			before, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}

			w := putConfigRaw(t, cs, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			// The message must name the field: "invalid request body" would send the user to
			// the whole form when one control is wrong.
			if field := firstKey(t, tc.body); !strings.Contains(w.Body.String(), field) {
				t.Errorf("error %q does not name the offending field %q", w.Body.String(), field)
			}

			after, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config after rejection: %v", err)
			}
			if !sameConfig(before, after) {
				t.Errorf("body %s was rejected but the stored config moved:\n before %+v\n after  %+v", tc.body, before, after)
			}
		})
	}
}

// TestPUTConfig_NullMeansAbsent: a JSON null carries nothing to write, so it means "not sent" -
// for every field, not just the providers list that had its own nil check.
func TestPUTConfig_NullMeansAbsent(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	seed := `{"providers":[{"name":"llm","kind":"llm","url":"http://localhost:8080/v1","models":[{"name":"m"}],"concurrency":1}],"worker_provider":"llm","worker_model":"m","utility_provider":"llm","utility_model":"m","default_auto_commit":true}`
	if w := putConfigRaw(t, cs, seed); w.Code != http.StatusOK {
		t.Fatalf("seed PUT: status %d body %s", w.Code, w.Body.String())
	}

	if w := putConfigRaw(t, cs, `{"providers":null,"worker_provider":null,"default_auto_commit":null}`); w.Code != http.StatusOK {
		t.Fatalf("null PUT: status %d body %s", w.Code, w.Body.String())
	}
	after, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(after.Providers) != 1 {
		t.Errorf("providers = %d after a null, want the stored 1 untouched", len(after.Providers))
	}
	if after.WorkerProvider != "llm" {
		t.Errorf("worker_provider = %q after a null, want llm", after.WorkerProvider)
	}
	if after.DefaultAutoCommit == nil || !*after.DefaultAutoCommit {
		t.Errorf("default_auto_commit = %v after a null, want the stored true", after.DefaultAutoCommit)
	}
}

// TestPUTConfig_PreservesStoredAPIKeysWhenNotSent: the merge rule behind mergeProviderKeys -
// nil = preserve, "" = clear, non-empty = replace.
func TestPUTConfig_PreservesStoredAPIKeysWhenNotSent(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	seed := `{"providers":[{"name":"llm","kind":"llm","url":"http://localhost:8080/v1","models":[{"name":"m"}],"concurrency":1,"api_key":"secret-key"}],"worker_provider":"llm","worker_model":"m","utility_provider":"llm","utility_model":"m"}`
	if w := putConfigRaw(t, cs, seed); w.Code != http.StatusOK {
		t.Fatalf("seed PUT: status %d body %s", w.Code, w.Body.String())
	}

	// The same provider, identified by its stable id, with no api_key field: the stored key
	// survives. The match is on id rather than name or URL precisely because both of those are
	// editable - renaming a provider must not read as "new provider, no key".
	if w := putConfigRaw(t, cs, `{"providers":[{"id":1,"name":"llm","kind":"llm","url":"http://localhost:8080/v1","models":[{"name":"m"}],"concurrency":1}]}`); w.Code != http.StatusOK {
		t.Fatalf("update PUT: status %d body %s", w.Code, w.Body.String())
	}
	after, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// The preserve is about the stored row, and GetConfig is not where a key is visible any more.
	if len(after.Providers) != 1 || after.Providers[0].APIKey != nil {
		t.Fatalf("GetConfig handed a key out: %+v", after.Providers)
	}
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != "secret-key" {
		t.Fatalf("stored API key did not survive a providers payload that omitted it: %q", got)
	}
}

// TestPUTConfig_ThresholdIsClampedNotRejected records the deliberate split: a wrong TYPE is the
// client's mistake and gets a 400, an out-of-range number is clamped. The reader
// (AutoSummarizeThreshold) already treats an out-of-range stored value as unset, so clamping
// keeps what the UI displays equal to what gets stored instead of storing a value that will be
// ignored on read.
func TestPUTConfig_ThresholdIsClampedNotRejected(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	if w := putConfigRaw(t, cs, `{"default_auto_summarize_threshold":5000}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d body %s", w.Code, w.Body.String())
	}
	after, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if after.DefaultAutoSummarizeThreshold == nil || *after.DefaultAutoSummarizeThreshold != 100 {
		t.Fatalf("threshold = %v, want clamped to 100", after.DefaultAutoSummarizeThreshold)
	}
}

// TestGetConfig_CoercesStoredModeOnly: reading a row this build cannot have written must not
// fail, so an unparseable stored mode falls back to act - while the PUT handler refuses the same
// value. Different policies for different directions, on purpose.
func TestGetConfig_CoercesStoredModeOnly(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	if err := db.SetConfigValue("default_operating_mode", "autocomplete"); err != nil {
		t.Fatalf("seed mode: %v", err)
	}
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.DefaultOperatingMode == nil || *cfg.DefaultOperatingMode != types.ModeAct {
		t.Fatalf("DefaultOperatingMode = %v, want act coerced from an unparseable stored value", cfg.DefaultOperatingMode)
	}
}

// TestPUTConfig_AbsentFieldsKeepStoredValues is the partial-update contract itself: a save that
// mentions one control must not reset the others, which is what a settings form with one dirty
// field depends on.
func TestPUTConfig_AbsentFieldsKeepStoredValues(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	seed := `{"worker_provider":"wp","worker_model":"wm","utility_provider":"up","utility_model":"um",
		"default_auto_commit":true,"default_auto_merge":true,"default_subtasks_auto_merge":true,
		"default_operating_mode":"plan","default_max_failed_turns":5,
		"default_auto_summarize_threshold":42,"hide_model_badge":true,
		"run_siblings_parallel":true,"sort_oldest_first":true,"auto_maximize_chat_mobile":false,
		"reclaim_idle_sandbox":true}`
	if w := putConfigRaw(t, cs, seed); w.Code != http.StatusOK {
		t.Fatalf("seed PUT: status %d body %s", w.Code, w.Body.String())
	}
	before, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}

	if w := putConfigRaw(t, cs, `{"worker_model":"wm2"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d body %s", w.Code, w.Body.String())
	}
	after, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if after.WorkerModel != "wm2" {
		t.Errorf("worker_model = %q, want the value the body sent", after.WorkerModel)
	}
	if !sameConfigExcept(before, after, "worker_model") {
		t.Errorf("a one-field save changed other fields:\n before %+v\n after  %+v", before, after)
	}

	// And an explicit false must not read as absent.
	if w := putConfigRaw(t, cs, `{"hide_model_badge":false}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d body %s", w.Code, w.Body.String())
	}
	after, err = cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if after.HideModelBadge == nil || *after.HideModelBadge {
		t.Errorf("hide_model_badge = %v, want the explicit false the body sent", after.HideModelBadge)
	}
}

// firstKey returns the JSON object's first key, which is the field each case here gets wrong.
func firstKey(t *testing.T, body string) string {
	t.Helper()
	rest, _, _ := strings.Cut(strings.TrimPrefix(body, "{\""), "\"")
	if rest == "" {
		t.Fatalf("test body %q has no key to name", body)
	}
	return rest
}

// sameConfig compares the fields these tests touch. It is deliberately not reflect.DeepEqual:
// the response-derived fields (version, workspace dir) are not what a rejection may move, and a
// full compare would make the test fail for reasons unrelated to the assertion.
func sameConfig(a, b *types.Config) bool {
	return sameConfigExcept(a, b)
}

func sameConfigExcept(a, b *types.Config, ignore ...string) bool {
	skip := make(map[string]bool, len(ignore))
	for _, f := range ignore {
		skip[f] = true
	}
	eqStr := func(name, x, y string) bool { return skip[name] || x == y }
	eqBoolPtr := func(name string, x, y *bool) bool {
		if skip[name] || (x == nil) != (y == nil) {
			return false
		}
		return x == nil || *x == *y
	}
	eqIntPtr := func(name string, x, y *int) bool {
		if skip[name] || (x == nil) != (y == nil) {
			return false
		}
		return x == nil || *x == *y
	}
	eqModePtr := func(name string, x, y *types.OperatingMode) bool {
		if skip[name] || (x == nil) != (y == nil) {
			return false
		}
		return x == nil || *x == *y
	}
	return eqStr("worker_provider", a.WorkerProvider, b.WorkerProvider) &&
		eqStr("worker_model", a.WorkerModel, b.WorkerModel) &&
		eqStr("utility_provider", a.UtilityProvider, b.UtilityProvider) &&
		eqStr("utility_model", a.UtilityModel, b.UtilityModel) &&
		eqBoolPtr("default_auto_commit", a.DefaultAutoCommit, b.DefaultAutoCommit) &&
		eqBoolPtr("default_auto_merge", a.DefaultAutoMerge, b.DefaultAutoMerge) &&
		eqBoolPtr("default_subtasks_auto_merge", a.DefaultSubtasksAutoMerge, b.DefaultSubtasksAutoMerge) &&
		eqModePtr("default_operating_mode", a.DefaultOperatingMode, b.DefaultOperatingMode) &&
		eqIntPtr("default_max_failed_turns", a.DefaultMaxFailedTurns, b.DefaultMaxFailedTurns) &&
		eqIntPtr("default_auto_summarize_threshold", a.DefaultAutoSummarizeThreshold, b.DefaultAutoSummarizeThreshold) &&
		eqBoolPtr("hide_model_badge", a.HideModelBadge, b.HideModelBadge) &&
		eqBoolPtr("run_siblings_parallel", a.RunSiblingsParallel, b.RunSiblingsParallel) &&
		eqBoolPtr("sort_oldest_first", a.SortOldestFirst, b.SortOldestFirst) &&
		eqBoolPtr("auto_maximize_chat_mobile", a.AutoMaximizeChatMobile, b.AutoMaximizeChatMobile) &&
		eqBoolPtr("reclaim_idle_sandbox", a.ReclaimIdleSandbox, b.ReclaimIdleSandbox) &&
		len(a.Providers) == len(b.Providers)
}
