package exec

import "kanban/backend/types"

// estimateTaskMemory returns the MB the supervisor attributes to a task.
// Falls back to avgPeakMB when the task has no recorded peak (first run).
func estimateTaskMemory(task types.Task, avgPeakMB int64) int64 {
	if task.SandboxMemoryPeakMB > 0 {
		return task.SandboxMemoryPeakMB
	}
	return avgPeakMB
}

// computeAvgPeakMB returns the mean SandboxMemoryPeakMB across tasks with a recorded peak.
// Returns 0 when no task qualifies.
func computeAvgPeakMB(tasks []types.Task) int64 {
	var sum, n int64
	for _, t := range tasks {
		if t.SandboxMemoryPeakMB > 0 {
			sum += t.SandboxMemoryPeakMB
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}
