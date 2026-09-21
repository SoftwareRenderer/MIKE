package stream

import "mike/backend/types"

// TaskForBrowser returns a copy of the task row with `output` cleared.
//
// `output` is the text the agent reported when its turn ended. It is the largest field a row has, and a row
// goes to every tab of its project on every status change. Nothing in the frontend reads it: the chat shows
// the thread's messages, and the token frames already carried those words.
//
// The field is cleared rather than dropped, so a row always carries the same keys. The token-scoped read of
// one task still answers in full (docs/api.md).
//
// The copy matters: the row handed in belongs to the store or to a running turn.
func TaskForBrowser(task *types.Task) *types.Task {
	if task == nil {
		return nil
	}
	row := *task
	row.Output = ""
	return &row
}

// BrowserRows applies TaskForBrowser to a whole board, so the board route and the task_updated frame always
// send the same row.
func BrowserRows(tasks []types.Task) []types.Task {
	rows := make([]types.Task, 0, len(tasks))
	for i := range tasks {
		rows = append(rows, *TaskForBrowser(&tasks[i]))
	}
	return rows
}
