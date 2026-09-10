package store

import (
	"database/sql"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

// GetProjectStats returns aggregated token usage for a project, grouped by model/provider.
// Reads from the cumulative project_token_stats table (survives task deletion).
func (s *Store) GetProjectStats(projectID string) (*types.ProjectStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &types.ProjectStats{}

	// Get total task count for the project (tasks with LLM usage only, matching per-model task_count)
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT task_id) FROM project_token_tasks WHERE project_id = ?`, projectID).Scan(&stats.TotalTasks)
	if err != nil {
		return nil, err
	}

	// Read cumulative stats from project_token_stats table.
	// The conditional sums (with_timing) only include entries where timing data is available,
	// so overall throughput isn't skewed by agent providers that report 0ms.
	rows, err := s.db.Query(`
		SELECT
			model_name,
			provider_name,
			prompt_tokens,
			cache_tokens,
			prompt_tokens - cache_tokens as processed_tokens,
			predicted_tokens,
			CASE WHEN prompt_ms > 0 THEN (prompt_tokens - cache_tokens) * 1000.0 / prompt_ms ELSE 0 END as avg_prompt_tps,
			CASE WHEN predicted_ms > 0 THEN predicted_tokens * 1000.0 / predicted_ms ELSE 0 END as avg_generation_tps,
			prompt_ms,
			predicted_ms,
			cost,
			SUM(CASE WHEN prompt_ms > 0 THEN prompt_tokens - cache_tokens ELSE 0 END) OVER () as total_processed_with_timing,
			SUM(CASE WHEN predicted_ms > 0 THEN predicted_tokens ELSE 0 END) OVER () as total_generated_with_timing
		FROM project_token_stats
		WHERE project_id = ?
		ORDER BY predicted_tokens DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var totalPromptMs, totalGenMs int64
	var totalProcessedWithTiming, totalGeneratedWithTiming int64

	for rows.Next() {
		var ms types.ModelStats
		var promptMsRow int64
		err := rows.Scan(&ms.ModelName, &ms.ProviderName, &ms.PromptTokens, &ms.CacheTokens,
			&ms.ProcessedTokens, &ms.GeneratedTokens, &ms.AvgPromptTps, &ms.AvgGenerationTps,
			&promptMsRow, &ms.PredictedMs, &ms.Cost,
			&totalProcessedWithTiming, &totalGeneratedWithTiming)
		if err != nil {
			continue
		}
		stats.ByModel = append(stats.ByModel, ms)

		stats.TotalPromptTokens += ms.PromptTokens
		stats.TotalCacheTokens += ms.CacheTokens
		stats.TotalProcessedTokens += ms.ProcessedTokens
		stats.TotalGeneratedTokens += ms.GeneratedTokens
		stats.TotalCost += ms.Cost

		totalPromptMs += promptMsRow
		totalGenMs += ms.PredictedMs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute overall throughput using only entries with timing data.
	// Agent providers (e.g. Pi) report 0ms, so including their tokens would
	// artificially inflate throughput since they contribute tokens without time.
	if totalPromptMs > 0 {
		stats.AvgPromptTps = float64(totalProcessedWithTiming) * 1000.0 / float64(totalPromptMs)
	}
	if totalGenMs > 0 {
		stats.AvgGenerationTps = float64(totalGeneratedWithTiming) * 1000.0 / float64(totalGenMs)
	}
	stats.TotalProcessedTokensWithTiming = int(totalProcessedWithTiming)
	stats.TotalGeneratedTokensWithTiming = int(totalGeneratedWithTiming)

	// Compute task_count per model from cumulative task tracking table.
	taskRows, err := s.db.Query(`
		SELECT model_name, provider_name, COUNT(task_id) as task_count
		FROM project_token_tasks
		WHERE project_id = ?
		GROUP BY model_name, provider_name
	`, projectID)
	if err == nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var mName, pName string
			var tCount int
			if err := taskRows.Scan(&mName, &pName, &tCount); err != nil {
				continue
			}
			for i := range stats.ByModel {
				if stats.ByModel[i].ModelName == mName && stats.ByModel[i].ProviderName == pName {
					stats.ByModel[i].TaskCount = tCount
					break
				}
			}
		}
		// A truncated join would show as an unexplained 0; the token totals are complete.
		if err := taskRows.Err(); err != nil {
			logging.For(logging.Store).With(logging.ProjectID, projectID).Infof("task counts incomplete: %v", err)
		}
	}

	return stats, nil
}

// AccumulateProjectStats increments the cost for a specific model/provider in a project.
// This is used by agent providers to record the session cost reported by the agent,
// since agents report their own cost rather than having it computed from token counts.
func (s *Store) AccumulateProjectStats(projectID, modelName, providerName string, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Use INSERT ... ON CONFLICT so the row is created if it doesn't exist yet
	// (e.g. agent task with no prior assistant message persistence).
	_, _ = s.db.Exec(`
		INSERT INTO project_token_stats (project_id, model_name, provider_name, cost)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, model_name, provider_name)
		DO UPDATE SET cost = project_token_stats.cost + excluded.cost`,
		projectID, modelName, providerName, cost)
}

// DeleteProjectStats zeros out the cumulative stats for a specific model/provider in a project.
// If providerName is empty, it matches any provider for that model.
// This does NOT delete messages from task_messages.
// DeleteProjectStats zeroes a model's cumulative counters and drops the per-task rows behind them. One
// transaction: counters zeroed with their task rows still present is a table that adds up to nothing, and
// the two statements take the same predicate, so they either both apply or neither does.
func (s *Store) DeleteProjectStats(projectID, modelName, providerName string) error {
	where := `project_id = ? AND model_name = ?`
	args := []any{projectID, modelName}
	if providerName != "" {
		where += ` AND provider_name = ?`
		args = append(args, providerName)
	}
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			UPDATE project_token_stats
			SET prompt_tokens = 0, cache_tokens = 0, predicted_tokens = 0, prompt_ms = 0, predicted_ms = 0, cost = 0
			WHERE `+where, args...); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM project_token_tasks WHERE `+where, args...)
		return err
	})
}
