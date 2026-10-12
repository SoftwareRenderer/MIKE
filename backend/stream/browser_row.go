package stream

import "mike/backend/types"

// TaskForBrowser returns a copy of the task row with Output cleared.
// The field is cleared rather than dropped to preserve the key set.
// The token-scoped read of one task still answers in full (docs/api.md).
func TaskForBrowser(task *types.Task) *types.Task {
	if task == nil {
		return nil
	}
	row := *task
	row.Output = ""
	return &row
}

// BrowserRows returns a board of tasks, each processed by TaskForBrowser().
func BrowserRows(tasks []types.Task) []types.Task {
	rows := make([]types.Task, 0, len(tasks))
	for i := range tasks {
		rows = append(rows, *TaskForBrowser(&tasks[i]))
	}
	return rows
}
