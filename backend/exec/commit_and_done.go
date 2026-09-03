package exec

// A "Commit and move to done" click asks for commit, merge and Done; a merge conflict only
// interrupts it, so the turn that resolves the conflict finishes the click. Recorded in memory
// because only the click knows it was the click - one found at task start means "keep working" - so a
// restart drops it, and so does the user taking over.

// ArmCommitAndDone records that a "Commit and move to done" click produced this task's merge
// conflict. Idempotent: clicking again states the same intent.
func (te *TaskExecutor) ArmCommitAndDone(taskID string) {
	te.commitDone.mu.Lock()
	defer te.commitDone.mu.Unlock()
	// Created here, not in NewTaskExecutor: tests build a TaskExecutor as a struct literal.
	if te.commitDone.tasks == nil {
		te.commitDone.tasks = make(map[string]struct{})
	}
	te.commitDone.tasks[taskID] = struct{}{}
}

// CommitAndDonePending reports whether a click is recorded, without consuming it. Read it to decide
// whether a merge is wanted.
func (te *TaskExecutor) CommitAndDonePending(taskID string) bool {
	te.commitDone.mu.Lock()
	defer te.commitDone.mu.Unlock()
	_, ok := te.commitDone.tasks[taskID]
	return ok
}

// takeCommitAndDone consumes the click and reports whether the task is done by it: a click was
// recorded AND mergeOK. A failed merge spends the click too, and this may only be called once the
// conflict is over - a still conflicted resolution needs it for the turn that finishes.
func (te *TaskExecutor) takeCommitAndDone(taskID string, mergeOK bool) bool {
	te.commitDone.mu.Lock()
	defer te.commitDone.mu.Unlock()
	if _, ok := te.commitDone.tasks[taskID]; !ok {
		return false
	}
	delete(te.commitDone.tasks, taskID)
	return mergeOK
}

// clearCommitAndDone drops the click without acting on it: the user has taken over, so what they said
// is now the intent.
func (te *TaskExecutor) clearCommitAndDone(taskID string) {
	te.commitDone.mu.Lock()
	defer te.commitDone.mu.Unlock()
	delete(te.commitDone.tasks, taskID)
}
