import type { ProjectStats } from '$lib/types';

/** One model's worth of usage, shared by the two Token Usage specs. */
export const STATS: ProjectStats = {
    total_tasks: 1,
    total_prompt_tokens: 10,
    total_cache_tokens: 0,
    total_processed_tokens: 10,
    total_generated_tokens: 5,
    avg_prompt_tps: 0,
    avg_generation_tps: 0,
    total_power_kwh: 0,
    total_cost: 0,
    by_model: [{
        model_name: 'gpt-test', provider_name: 'prov-a',
        prompt_tokens: 10, cache_tokens: 0, processed_tokens: 10, generated_tokens: 5,
        avg_prompt_tps: 0, avg_generation_tps: 0, power_kwh: 0, task_count: 1, cost: 0,
    }],
};
