package store

import (
	"kanban/backend/types"
)

// GetProjectStats returns aggregated token usage for a project, grouped by model/provider.
func (s *Store) GetProjectStats(projectID string) (*types.ProjectStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &types.ProjectStats{}

	// Get total task count for the project
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id = ?`, projectID).Scan(&stats.TotalTasks)
	if err != nil {
		return nil, err
	}

	// Get per-model/provider aggregation with t/s (tokens per second).
	// prompt_ms  = time to process prompt tokens
	// predicted_ms = time to generate (predict) completion tokens
	rows, err := s.db.Query(`
		SELECT
			COALESCE(model_name, 'unknown') as model_name,
			COALESCE(provider_name, '') as provider_name,
			SUM(prompt_tokens) as total_prompt_tokens,
			SUM(COALESCE(cache_tokens, 0)) as total_cache_tokens,
			SUM(prompt_tokens - COALESCE(cache_tokens, 0)) as total_processed_tokens,
			SUM(predicted_tokens) as total_generated_tokens,
			CASE WHEN SUM(prompt_ms) > 0 THEN SUM(prompt_tokens - COALESCE(cache_tokens, 0)) * 1000.0 / SUM(prompt_ms) ELSE 0 END as avg_prompt_tps,
			CASE WHEN SUM(predicted_ms) > 0 THEN SUM(predicted_tokens) * 1000.0 / SUM(predicted_ms) ELSE 0 END as avg_generation_tps,
			COUNT(DISTINCT task_id) as task_count,
			SUM(prompt_tokens - COALESCE(cache_tokens, 0)) as total_processed_prompt_tokens,
			SUM(prompt_ms) as total_prompt_ms,
			SUM(predicted_tokens) as total_predicted_tokens,
			SUM(predicted_ms) as total_predicted_ms
		FROM task_messages
		WHERE role = 'assistant'
			AND task_id IN (SELECT id FROM tasks WHERE project_id = ?)
		GROUP BY model_name, provider_name
		ORDER BY total_generated_tokens DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var totalProcessedPromptTokens, totalPromptMs int64
	var totalGenTokens, totalGenMs int64

	for rows.Next() {
		var ms types.ModelStats
		var processedPromptTokensRow, promptMsRow, genTokensRow, genMsRow int64
		err := rows.Scan(&ms.ModelName, &ms.ProviderName, &ms.PromptTokens, &ms.CacheTokens,
			&ms.ProcessedTokens, &ms.GeneratedTokens, &ms.AvgPromptTps, &ms.AvgGenerationTps,
			&ms.TaskCount, &processedPromptTokensRow, &promptMsRow, &genTokensRow, &genMsRow)
		if err != nil {
			continue
		}
		stats.ByModel = append(stats.ByModel, ms)

		stats.TotalPromptTokens += ms.PromptTokens
		stats.TotalCacheTokens += ms.CacheTokens
		stats.TotalProcessedTokens += ms.ProcessedTokens
		stats.TotalGeneratedTokens += ms.GeneratedTokens

		totalProcessedPromptTokens += processedPromptTokensRow
		totalPromptMs += promptMsRow
		totalGenTokens += genTokensRow
		totalGenMs += genMsRow
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute overall throughput: processed tokens / total time
	if totalPromptMs > 0 {
		stats.AvgPromptTps = float64(totalProcessedPromptTokens) * 1000.0 / float64(totalPromptMs)
	}
	if totalGenMs > 0 {
		stats.AvgGenerationTps = float64(totalGenTokens) * 1000.0 / float64(totalGenMs)
	}

	return stats, nil
}

// DeleteProjectStats removes assistant messages for a specific model/provider in a project.
// If providerName is empty, it matches any provider for that model.
func (s *Store) DeleteProjectStats(projectID, modelName, providerName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	taskSubquery := `SELECT id FROM tasks WHERE project_id = ?`

	if providerName != "" {
		_, err := s.db.Exec(`
			DELETE FROM task_messages
			WHERE role = 'assistant'
				AND task_id IN (`+taskSubquery+`)
				AND model_name = ?
				AND provider_name = ?`,
			projectID, modelName, providerName)
		return err
	}

	_, err := s.db.Exec(`
		DELETE FROM task_messages
		WHERE role = 'assistant'
			AND task_id IN (`+taskSubquery+`)
			AND model_name = ?`,
		projectID, modelName)
	return err
}
