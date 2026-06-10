package managers

import (
	"encoding/json"
	"net/http"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// ReviewHandler handles review comment operations.
type ReviewHandler struct {
	store *store.Store
}

// NewReviewHandler creates a new ReviewHandler.
func NewReviewHandler(store *store.Store) *ReviewHandler {
	return &ReviewHandler{store: store}
}

// HandleComments lists or creates review comments for a task.
func (h *ReviewHandler) HandleComments(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		comments, err := h.store.GetReviewComments(taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, comments)
	case http.MethodPost:
		var req types.CreateReviewCommentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		comment := &types.ReviewComment{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			FilePath:  req.FilePath,
			LineNum:   req.LineNum,
			Comment:   req.Comment,
			Resolved:  false,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := h.store.CreateReviewComment(comment); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusCreated, comment)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleComment handles GET/PUT/DELETE for a single review comment.
func (h *ReviewHandler) HandleComment(w http.ResponseWriter, r *http.Request, projectID, taskID, commentID string) {
	switch r.Method {
	case http.MethodPut:
		var req types.UpdateReviewCommentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		comment, err := h.store.UpdateReviewComment(commentID, req.Comment, req.Resolved)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, comment)
	case http.MethodDelete:
		if err := h.store.DeleteReviewComment(commentID); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
