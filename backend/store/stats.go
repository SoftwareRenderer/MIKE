package store

import (
	"kanban/backend/types"
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

	// Read cumulative stats from project_token_stats table
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
			predicted_ms
		FROM project_token_stats
		WHERE project_id = ?
		ORDER BY predicted_tokens DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var totalPromptMs, totalGenMs int64

	for rows.Next() {
		var ms types.ModelStats
		var promptMsRow int64
		err := rows.Scan(&ms.ModelName, &ms.ProviderName, &ms.PromptTokens, &ms.CacheTokens,
			&ms.ProcessedTokens, &ms.GeneratedTokens, &ms.AvgPromptTps, &ms.AvgGenerationTps,
			&promptMsRow, &ms.PredictedMs)
		if err != nil {
			continue
		}
		stats.ByModel = append(stats.ByModel, ms)

		stats.TotalPromptTokens += ms.PromptTokens
		stats.TotalCacheTokens += ms.CacheTokens
		stats.TotalProcessedTokens += ms.ProcessedTokens
		stats.TotalGeneratedTokens += ms.GeneratedTokens

		totalPromptMs += promptMsRow
		totalGenMs += ms.PredictedMs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute overall throughput: processed tokens / total time
	if totalPromptMs > 0 {
		stats.AvgPromptTps = float64(stats.TotalProcessedTokens) * 1000.0 / float64(totalPromptMs)
	}
	if totalGenMs > 0 {
		stats.AvgGenerationTps = float64(stats.TotalGeneratedTokens) * 1000.0 / float64(totalGenMs)
	}

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
	}

	return stats, nil
}

// DeleteProjectStats zeros out the cumulative stats for a specific model/provider in a project.
// If providerName is empty, it matches any provider for that model.
// This does NOT delete messages from task_messages.
func (s *Store) DeleteProjectStats(projectID, modelName, providerName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if providerName != "" {
		_, err := s.db.Exec(`
			UPDATE project_token_stats
			SET prompt_tokens = 0, cache_tokens = 0, predicted_tokens = 0, prompt_ms = 0, predicted_ms = 0
			WHERE project_id = ? AND model_name = ? AND provider_name = ?`,
			projectID, modelName, providerName)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`
			DELETE FROM project_token_tasks
			WHERE project_id = ? AND model_name = ? AND provider_name = ?`,
			projectID, modelName, providerName)
		return err
	}

	_, err := s.db.Exec(`
		UPDATE project_token_stats
		SET prompt_tokens = 0, cache_tokens = 0, predicted_tokens = 0, prompt_ms = 0, predicted_ms = 0
		WHERE project_id = ? AND model_name = ?`,
		projectID, modelName)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		DELETE FROM project_token_tasks
		WHERE project_id = ? AND model_name = ?`,
		projectID, modelName)
	return err
}
