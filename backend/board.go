package backend

import (
	"encoding/json"
	"net/http"
)

// BoardManager handles board-level operations.
type BoardManager struct {
	store *Store
}

// NewBoardManager creates a new BoardManager.
func NewBoardManager(store *Store) *BoardManager {
	return &BoardManager{store: store}
}

// HandleGetBoard returns the board (all tasks) for a project.
func (bm *BoardManager) HandleGetBoard(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify project exists
	if _, err := bm.store.GetProject(projectID); err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}

	tasks, err := bm.store.GetTasksByProject(projectID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}
	if tasks == nil {
		tasks = []Task{}
	}

	jsonResponse(w, http.StatusOK, Board{Tasks: tasks})
}

// HandleReorder bulk-reorders tasks within columns.
func (bm *BoardManager) HandleReorder(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify project exists
	if _, err := bm.store.GetProject(projectID); err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}

	var reorders []ReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&reorders); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := bm.store.ReorderTasks(projectID, reorders); err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to reorder tasks")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}