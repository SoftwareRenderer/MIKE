package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/config"
)

// The lock belongs to the process, not the request, so these tests re-point the handler's config store at
// the decision it would have booted on.

func lockStatReset(t *testing.T, h *ProjectHandler) {
	t.Helper()
	h.configStore = config.NewConfigStore(h.store, "", "test", nil, false, true)
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

func TestHandleStatsResetRefusedUnderTheLock(t *testing.T) {
	h, projID := seededHandler(t)
	lockStatReset(t, h)
	before := promptTokens(t, h, projID)
	if before == 0 {
		t.Fatal("fixture wrote no tokens; the test would pass on an empty table")
	}

	rr := resetStats(t, h, projID, "model-a", "prov-a")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("reset under %s = %d, want 403: %s", config.EnvRestrictStatReset, rr.Code, rr.Body.String())
	}
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
}

func TestHandleStatsResetStillResetsWithoutTheLock(t *testing.T) {
	h, projID := seededHandler(t)
	if promptTokens(t, h, projID) == 0 {
		t.Fatal("fixture wrote no tokens; the test would pass on an empty table")
	}

	if rr := resetStats(t, h, projID, "model-a", "prov-a"); rr.Code != http.StatusNoContent {
		t.Fatalf("reset without the lock = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if got := promptTokens(t, h, projID); got != 0 {
		t.Errorf("counters after a permitted reset = %d, want 0", got)
	}
}
