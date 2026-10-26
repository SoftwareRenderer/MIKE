package api

import (
	"encoding/json"
	"errors"

	"mike/backend/types"
)

// taskUpdateRequest handles partial updates to a task. Every field is a pointer so that
// absent keys are ignored, present keys are written (including "" and false), and JSON nulls
// are treated as absent. Nothing is written until every field validates.
type taskUpdateRequest struct {
	Title                     *string `json:"title"`
	Prompt                    *string `json:"prompt"`
	Status                    *string `json:"status"`
	SessionID                 *string `json:"session_id"`
	Branch                    *string `json:"branch"`
	BaseRef                   *string `json:"base_ref"`
	BranchTip                 *string `json:"branch_tip"`
	Output                    *string `json:"output"`
	ExecutionState            *string `json:"execution_state"`
	HasMergeConflict          *bool   `json:"has_merge_conflict"`
	LlamaServerURL            *string `json:"llama_server_url"`
	ModelName                 *string `json:"model_name"`
	ReasoningEffort           *string `json:"reasoning_effort"`
	MaxConsecutiveFailedTurns *int    `json:"max_consecutive_failed_turns"`
	AutoCommit                *bool   `json:"auto_commit"`
	AutoMerge                 *bool   `json:"auto_merge"`
	SubtasksAutoMerge         *bool   `json:"subtasks_auto_merge"`
	OperatingMode             *string `json:"operating_mode"`
	ParentTaskID              *string `json:"parent_task_id"`
	TitleGenerating           *bool   `json:"title_generating"`
	CurrentThreadID           *string `json:"current_thread_id"`
}

// autoMergeFieldNames are the fields ApplyTaskCreationDefaults may overwrite when a task's
// parent changes - the caller must tell it which ones the client sent itself.
var autoMergeFieldNames = []string{"auto_commit", "auto_merge", "subtasks_auto_merge"}

// autoMergeFieldsSent reports which of autoMergeFieldNames the client sent, in the shape
// ApplyTaskCreationDefaults wants for its skip set.
func (u *taskUpdateRequest) autoMergeFieldsSent() map[string]struct{} {
	sent := make(map[string]struct{})
	for _, name := range autoMergeFieldNames {
		var provided bool
		switch name {
		case "auto_commit":
			provided = u.AutoCommit != nil
		case "auto_merge":
			provided = u.AutoMerge != nil
		case "subtasks_auto_merge":
			provided = u.SubtasksAutoMerge != nil
		}
		if provided {
			sent[name] = struct{}{}
		}
	}
	return sent
}

// apply reports whether operating_mode changed, which is needed to remount a container
// that is not running. Every validation runs before anything is written, so a rejected
// body leaves the task exactly as it was found.
func (u *taskUpdateRequest) apply(task *types.Task) (operatingModeChanged bool, err error) {
	// Validate first: a bad status leaves a card on no column, a bad execution_state a card
	// with no spinner, and a bad mode a task with write tools the user believes is read-only.
	var status types.Status
	if u.Status != nil {
		var valid bool
		if status, valid = types.ParseStatus(*u.Status); !valid {
			return false, badField("unknown status")
		}
	}
	var execState types.ExecutionState
	if u.ExecutionState != nil {
		var valid bool
		if execState, valid = types.ParseExecutionState(*u.ExecutionState); !valid {
			return false, badField("unknown execution state")
		}
	}
	for _, ref := range []*string{u.Branch, u.BaseRef, u.BranchTip} {
		if ref == nil {
			continue
		}
		if vErr := types.ValidateBranchName(*ref); vErr != nil {
			return false, badField(vErr.Error())
		}
	}
	var mode types.OperatingMode
	if u.OperatingMode != nil {
		var valid bool
		if mode, valid = types.ParseOperatingMode(*u.OperatingMode); !valid {
			return false, badField("unknown operating mode")
		}
	}

	// Nothing below this line can fail.
	if u.Title != nil {
		task.Title = *u.Title
		task.TitleLLM = false
	}
	if u.Prompt != nil {
		task.Prompt = *u.Prompt
	}
	if u.Status != nil {
		task.Status = status
	}
	if u.SessionID != nil {
		task.SessionID = *u.SessionID
	}
	if u.Branch != nil {
		task.Branch = *u.Branch
	}
	if u.BaseRef != nil {
		task.BaseRef = *u.BaseRef
	}
	if u.BranchTip != nil {
		task.BranchTip = *u.BranchTip
	}
	if u.Output != nil {
		task.Output = *u.Output
	}
	if u.ExecutionState != nil {
		task.ExecutionState = execState
	}
	if u.HasMergeConflict != nil {
		task.HasMergeConflict = *u.HasMergeConflict
	}
	if u.LlamaServerURL != nil {
		task.LlamaServerURL = *u.LlamaServerURL
	}
	if u.ModelName != nil {
		task.ModelName = *u.ModelName
	}
	if u.ReasoningEffort != nil {
		task.ReasoningEffort = *u.ReasoningEffort
	}
	if u.MaxConsecutiveFailedTurns != nil {
		task.MaxConsecutiveFailedTurns = *u.MaxConsecutiveFailedTurns
	}
	if u.AutoCommit != nil {
		task.AutoCommit = *u.AutoCommit
	}
	if u.AutoMerge != nil {
		task.AutoMerge = *u.AutoMerge
	}
	if u.SubtasksAutoMerge != nil {
		task.SubtasksAutoMerge = *u.SubtasksAutoMerge
	}
	if u.ParentTaskID != nil {
		task.ParentTaskID = *u.ParentTaskID
	}
	if u.TitleGenerating != nil {
		task.TitleGenerating = *u.TitleGenerating
	}
	if u.CurrentThreadID != nil {
		task.CurrentThreadID = *u.CurrentThreadID
	}
	if u.OperatingMode != nil {
		operatingModeChanged = mode != task.OperatingMode
		task.OperatingMode = mode
	}
	return operatingModeChanged, nil
}

// badField returns an error message written for the client, so apply never
// leaks internal errors into a response.
func badField(msg string) error { return errors.New(msg) }

// fieldsPresent identifies sent keys to distinguish between zero values and omitted fields.
// It uses json.RawMessage so attachments are decoded rather than asserted, preventing
// panics on unexpected types.
type fieldsPresent map[string]json.RawMessage

func (f fieldsPresent) has(key string) bool {
	_, ok := f[key]
	return ok
}

// decode reports false when key is absent or null. A value that will not decode is
// the client's mistake, and the error names the field.
func (f fieldsPresent) decode(key string, target any) (bool, error) {
	raw, ok := f[key]
	if !ok || string(raw) == "null" {
		return false, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false, badField("invalid " + key)
	}
	return true, nil
}
