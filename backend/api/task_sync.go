package api

import (
	"time"

	"mike/backend/types"
)

// taskSyncResponse reports changes to a task view.
type taskSyncResponse struct {
	Status           types.Status         `json:"status"`
	ExecutionState   types.ExecutionState `json:"execution_state"`
	Summarizing      bool                 `json:"summarizing"`
	HasMergeConflict bool                 `json:"has_merge_conflict"`
	// BranchTip is what a done task's diff is computed against.
	BranchTip string    `json:"branch_tip,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// a thread switch replaces a screen's whole window.
	Thread       string               `json:"thread,omitempty"`
	Conversation taskSyncConversation `json:"conversation"`
	Diff         taskSyncDiff         `json:"diff"`
}

type taskSyncConversation struct {
	// Epoch names this server process, so a revision the client cannot match is a re-read.
	Epoch string `json:"epoch"`
	// Revision counts durable conversation writes. See stream.AppStreamManager.ConversationRevision.
	Revision uint64 `json:"revision"`
	// Messages counts the rows, which a fork's copied thread moves and no durability seam sees.
	Messages int `json:"messages"`
}

// taskSyncDiff: Known=false means this process never computed one, which is
// an unknown and never a "clean".
type taskSyncDiff struct {
	Known  bool   `json:"known"`
	Hash   string `json:"hash,omitempty"`
	Staged int    `json:"staged"`
}

func (h *TaskHandler) syncDigest(task *types.Task) (taskSyncResponse, error) {
	rows, err := h.store.CountTaskMessages(task.ID)
	if err != nil {
		return taskSyncResponse{}, err
	}
	res := taskSyncResponse{
		Status:           task.Status,
		ExecutionState:   task.ExecutionState,
		Summarizing:      task.Summarizing,
		HasMergeConflict: task.HasMergeConflict,
		BranchTip:        task.BranchTip,
		UpdatedAt:        task.UpdatedAt,
		Thread:           task.CurrentThreadID,
		Conversation:     taskSyncConversation{Messages: rows},
	}
	if h.boardStreamMgr != nil {
		res.Conversation.Epoch = h.boardStreamMgr.ConversationEpoch()
		res.Conversation.Revision = h.boardStreamMgr.ConversationRevision(task.ProjectID, task.ID)
		if hash, staged, known := h.boardStreamMgr.TaskDiffDigest(task.ID); known {
			res.Diff = taskSyncDiff{Known: true, Hash: hash, Staged: staged}
		}
	}
	return res, nil
}
