package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/config"
)

// The lock belongs to the process, not the request, so these tests re-point the handler's config store at
// the decision it would have booted on.

func lockStatReset(t *testing.T, h *ProjectHandler, locked bool) {
	t.Helper()
	h.configStore = config.NewConfigStore(h.store, "", "test", nil, false, locked)
}

// resetStats sends the reset through the registered route: the handler reads the project from the path
// value the mux fills, so a direct call would reset the empty project id and report success.
func resetStats(t *testing.T, h *ProjectHandler, projID, model, provider string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/projects/{projectID}/stats", h.HandleStats)
	target := "/api/projects/" + projID + "/stats?model_name=" + model
	if provider != "" {
		target += "&provider_name=" + provider
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, target, nil))
	return rr
}

func seededHandler(t *testing.T) (*ProjectHandler, string) {
	t.Helper()
	h, projID := testProjectHandler(t)
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-a", 1000, 500, 100, 1000, 5000)
	return h, projID
}

func promptTokens(t *testing.T, h *ProjectHandler, projID string) int {
	t.Helper()
	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	return stats.TotalPromptTokens
}

// A refused reset is the whole point of the lock, so it answers 403 naming the variable and leaves the
// counters alone; without it the same request empties them.
func TestHandleStatsResetFollowsTheOperatorsLock(t *testing.T) {
	cases := []struct {
		name   string
		locked bool
		want   int
		check  func(t *testing.T, h *ProjectHandler, projID string, rr *httptest.ResponseRecorder, before int)
	}{
		{"locked", true, http.StatusForbidden, func(t *testing.T, h *ProjectHandler, projID string, rr *httptest.ResponseRecorder, before int) {
			var body map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal refusal: %v (%s)", err, rr.Body.String())
			}
			if !strings.Contains(body["error"], config.EnvRestrictStatReset) {
				t.Errorf("refusal does not name %s: %q", config.EnvRestrictStatReset, body["error"])
			}
			if got := promptTokens(t, h, projID); got != before {
				t.Errorf("a refused reset changed the counters from %d to %d", before, got)
			}
		}},
		{"unlocked", false, http.StatusNoContent, func(t *testing.T, h *ProjectHandler, projID string, _ *httptest.ResponseRecorder, _ int) {
			if got := promptTokens(t, h, projID); got != 0 {
				t.Errorf("counters after a permitted reset = %d, want 0", got)
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h, projID := seededHandler(t)
			lockStatReset(t, h, tt.locked)
			before := promptTokens(t, h, projID)
			if before == 0 {
				t.Fatal("fixture wrote no tokens; the test would pass on an empty table")
			}

			rr := resetStats(t, h, projID, "model-a", "prov-a")
			if rr.Code != tt.want {
				t.Fatalf("reset with the lock %v = %d, want %d: %s", tt.locked, rr.Code, tt.want, rr.Body.String())
			}
			tt.check(t, h, projID, rr, before)
		})
	}
}
