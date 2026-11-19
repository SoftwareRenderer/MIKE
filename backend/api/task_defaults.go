package api

// The config defaults a new task takes when the request names none, shared by the two server-side create
// paths.

import (
	"mike/backend/config"
	"mike/backend/types"
)

// applyTaskDefaults fills what a created task inherits: the configured worker pair when the task names
// neither half, the default operating mode, the default failed-turn limit, and the auto-commit/auto-merge
// trio, which a parent answers ahead of the config.
//
// A field named in skip is left as it arrived, while the pair answers to empty instead — the start path
// reads an empty URL as nothing configured.
func applyTaskDefaults(task *types.Task, configStore *config.ConfigStore, parent *types.Task, skip map[string]struct{}) {
	if configStore == nil {
		return
	}
	cfg, err := configStore.GetConfig()
	if err != nil {
		return
	}

	if task.LlamaServerURL == "" && task.ModelName == "" {
		if url, model, ok := configStore.GetWorkerTarget(); ok {
			task.LlamaServerURL, task.ModelName = url, model
		}
	}
	if _, ok := skip["operating_mode"]; !ok && cfg.DefaultOperatingMode != nil {
		task.OperatingMode = *cfg.DefaultOperatingMode
	}
	if task.OperatingMode == "" {
		task.OperatingMode = types.ModeAct
	}
	if _, ok := skip["max_consecutive_failed_turns"]; !ok && cfg.DefaultMaxFailedTurns != nil {
		task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
	}

	types.ApplyTaskCreationDefaults(task, cfg, parent, skip)
}
