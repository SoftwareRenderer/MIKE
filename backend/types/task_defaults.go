package types

// ApplyTaskCreationDefaults sets auto_commit, auto_merge, and subtasks_auto_merge
// on a new task using config defaults and parent inheritance.
//
// Order: config defaults → parent inheritance (if parent provided) → prerequisite guard.
//
// If skipFields is non-nil, fields present in it are not overwritten (allows the
// caller to preserve explicit client-provided values). Pass nil to always apply.
func ApplyTaskCreationDefaults(task *Task, cfg *Config, parent *Task, skipFields map[string]struct{}) {
	// Apply config defaults
	if cfg != nil {
		if cfg.DefaultAutoCommit != nil {
			if skipFields == nil || !hasSkipField(skipFields, "auto_commit") {
				task.AutoCommit = *cfg.DefaultAutoCommit
			}
		}
		if cfg.DefaultAutoMerge != nil {
			if skipFields == nil || !hasSkipField(skipFields, "auto_merge") {
				task.AutoMerge = *cfg.DefaultAutoMerge
			}
		}
		if cfg.DefaultSubtasksAutoMerge != nil {
			if skipFields == nil || !hasSkipField(skipFields, "subtasks_auto_merge") {
				task.SubtasksAutoMerge = *cfg.DefaultSubtasksAutoMerge
			}
		}
	}

	// For child tasks, inherit auto_commit/auto_merge from parent
	if parent != nil {
		if skipFields == nil || !hasSkipField(skipFields, "auto_commit") {
			task.AutoCommit = parent.AutoCommit
		}
		if skipFields == nil || !hasSkipField(skipFields, "auto_merge") {
			task.AutoMerge = parent.SubtasksAutoMerge
		}
	}

	// Auto-commit is a prerequisite for auto-merge — ensure consistency
	if task.AutoMerge {
		task.AutoCommit = true
	}
}

func hasSkipField(fields map[string]struct{}, name string) bool {
	_, ok := fields[name]
	return ok
}
