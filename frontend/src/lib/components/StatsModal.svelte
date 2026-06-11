<script lang="ts">
    import { onMount } from 'svelte';
    import type { ProjectStats, ModelStats } from '$lib/types';
    import { getProjectStats, resetProjectStats } from '$lib/api';
    import { showStats } from '$lib/store';
    import { createFocusTrap } from '$lib/a11y';

    let { projectId, onClose }: { projectId: string; onClose: () => void } = $props();

    let stats: ProjectStats | null = $state(null);
    let loading = $state(true);
    let error = $state('');
    let modalEl: HTMLDivElement | null = null;

    onMount(async () => {
        try {
            const data = await getProjectStats(projectId);
            stats = data;
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
        if (modalEl) {
            createFocusTrap(modalEl, handleClose);
            modalEl.focus();
        }
    });

    function handleClose() {
        showStats.set(false);
        onClose();
    }

    function formatNumber(n: number): string {
        return n.toLocaleString();
    }

    function formatTps(tps: number): string {
        if (tps === 0) return '-';
        if (tps >= 1000) {
            return `${(tps / 1000).toFixed(1)}k t/s`;
        }
        return `${Math.round(tps)} t/s`;
    }

    function getTokenLabel(model: ModelStats): string {
        if (model.provider_name === 'title_generation') {
            return `${model.model_name} (Title Generation)`;
        }
        if (model.provider_name) {
            return `${model.provider_name} / ${model.model_name}`;
        }
        return model.model_name;
    }

    // Sort model rows: regular entries first, title generation at bottom, filter out zeroed entries
    let sortedModels: ModelStats[] = $derived(
        (stats?.by_model || []).filter(m => m.task_count > 0 || m.generated_tokens > 0 || m.processed_tokens > 0 || m.cache_tokens > 0)
            .toSorted((a, b) => {
                const aTitle = a.provider_name === 'title_generation' ? 1 : 0;
                const bTitle = b.provider_name === 'title_generation' ? 1 : 0;
                if (aTitle !== bTitle) return aTitle - bTitle;
                return b.generated_tokens - a.generated_tokens;
            })
    );

    async function resetModel(model: ModelStats) {
        if (!confirm(`Reset token stats for ${getTokenLabel(model)}?`)) return;
        try {
            await resetProjectStats(projectId, model.model_name, model.provider_name || undefined);
            loading = true;
            error = '';
            const data = await getProjectStats(projectId);
            stats = data;
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }
</script>

<div class="overlay" onclick={handleClose} role="presentation">
    <div class="modal" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={modalEl} role="dialog" aria-modal="true" aria-labelledby="stats-modal-title" tabindex="-1">
        <div class="modal-header">
            <h2 id="stats-modal-title">Token Usage</h2>
            <button class="close-btn" onclick={handleClose} aria-label="Close stats">✕</button>
        </div>
        <div class="modal-body">
            {#if loading}
                <div class="loading">Loading stats...</div>
            {:else if error}
                <div class="error">{error}</div>
            {:else if !stats || sortedModels.length === 0}
                <div class="empty">No token usage data yet. Run a task to see stats here.</div>
            {:else}
                <!-- Overall summary -->
                <div class="summary-card">
                    <div class="summary-row">
                        <div class="summary-item">
                            <span class="summary-value">{stats.total_tasks}</span>
                            <span class="summary-label">Tasks</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_cache_tokens)}</span>
                            <span class="summary-label">Cached</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_processed_tokens)}</span>
                            <span class="summary-label">Prompt</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_generated_tokens)}</span>
                            <span class="summary-label">Generated</span>
                        </div>
                    </div>
                    <div class="summary-row timings">
                        <div class="summary-item">
                            <span class="summary-value">{formatTps(stats.avg_prompt_tps)}</span>
                            <span class="summary-label">Prompt Speed</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatTps(stats.avg_generation_tps)}</span>
                            <span class="summary-label">Gen Speed</span>
                        </div>
                    </div>
                </div>

                <!-- Per-model breakdown -->
                <table class="model-table">
                    <thead>
                        <tr>
                            <th class="col-model">Model</th>
                            <th class="col-num">Tasks</th>
                            <th class="col-num">Cached</th>
                            <th class="col-num">Prompt</th>
                            <th class="col-num">Generated</th>
                            <th class="col-num">Prompt Speed</th>
                            <th class="col-num">Gen Speed</th>
                            <th class="col-action"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each sortedModels as model}
                            <tr class:title-gen-row={model.provider_name === 'title_generation'}>
                                <td class="col-model">
                                    <span class="model-label" class:title-gen-label={model.provider_name === 'title_generation'}>{getTokenLabel(model)}</span>
                                </td>
                                <td class="col-num">{model.task_count}</td>
                                <td class="col-num">{formatNumber(model.cache_tokens)}</td>
                                <td class="col-num">{formatNumber(model.processed_tokens)}</td>
                                <td class="col-num">{formatNumber(model.generated_tokens)}</td>
                                <td class="col-num">{formatTps(model.avg_prompt_tps)}</td>
                                <td class="col-num">{formatTps(model.avg_generation_tps)}</td>
                                <td class="col-action">
                                    <button class="btn-reset" onclick={() => resetModel(model)} title="Reset stats for this model" aria-label={`Reset stats for ${getTokenLabel(model)}`}>
                                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <path d="M21 2v6h-6"/>
                                            <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                            <path d="M3 22v-6h6"/>
                                            <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                                        </svg>
                                        Reset
                                    </button>
                                </td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            {/if}
        </div>
    </div>
</div>

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.5);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 300;
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 90vw;
        max-width: 720px;
        max-height: 80dvh;
        display: flex;
        flex-direction: column;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
    }
    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 14px 20px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .modal-header h2 {
        font-size: 1rem;
        font-weight: 600;
    }
    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: 1.25rem;
        cursor: pointer;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
    }
    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .modal-body {
        padding: 20px;
        overflow-y: auto;
        flex: 1;
    }
    .loading, .empty {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 40px 20px;
    }
    .error {
        color: var(--color-status-red);
        text-align: center;
        padding: 20px;
    }

    /* Summary card */
    .summary-card {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
        margin-bottom: 20px;
    }
    .summary-row {
        display: flex;
        gap: 16px;
    }
    .summary-row + .summary-row {
        margin-top: 12px;
        padding-top: 12px;
        border-top: 1px solid var(--color-border);
    }
    .summary-item {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 4px;
    }
    .summary-value {
        font-size: 1.25rem;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        color: var(--color-text-primary);
    }
    .summary-label {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    /* Model table */
    .model-table {
        width: 100%;
        border-collapse: collapse;
        font-size: 0.8125rem;
    }
    .model-table th {
        text-align: left;
        padding: 8px 10px;
        font-weight: 600;
        color: var(--color-text-secondary);
        border-bottom: 2px solid var(--color-border);
        font-size: 0.75rem;
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .model-table td {
        padding: 8px 10px;
        border-bottom: 1px solid var(--color-border);
        font-variant-numeric: tabular-nums;
    }
    .model-table tbody tr:last-child td {
        border-bottom: none;
    }
    .model-table tbody tr:hover {
        background: var(--color-surface-2);
    }
    .col-model {
        text-align: left;
        min-width: 160px;
    }
    .col-num {
        text-align: right;
        width: 80px;
    }
    .col-action {
        text-align: center;
        width: 40px;
        padding: 8px 4px;
    }
    .btn-reset {
        background: none;
        border: 1px solid var(--color-border);
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 3px 8px;
        border-radius: var(--radius-sm);
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        font-family: var(--font-sans);
        transition: color 0.15s ease, background 0.15s ease, border-color 0.15s ease;
    }
    .btn-reset:hover {
        color: var(--color-status-red);
        background: rgba(248, 81, 73, 0.1);
        border-color: rgba(248, 81, 73, 0.3);
    }
    .model-label {
        font-family: var(--font-mono, monospace);
        font-size: 0.8125rem;
    }
    .title-gen-label {
        color: var(--color-text-secondary);
        font-style: italic;
    }
    .title-gen-row {
        background: var(--color-surface-2);
    }
    .title-gen-row:hover {
        background: var(--color-surface-3);
    }
    .title-gen-row td {
        border-top: 1px dashed var(--color-border);
    }

    @media (max-width: 768px) {
        .modal {
            width: 100vw;
            max-width: none;
            max-height: 100dvh;
            height: 100dvh;
            border-radius: 0;
            border: none;
        }
        .modal-header {
            padding: 16px;
        }
        .modal-body {
            padding: 16px;
        }
        .summary-row {
            flex-wrap: wrap;
        }
        .summary-item {
            min-width: 45%;
        }
        .model-table {
            font-size: 0.75rem;
        }
        .model-table th,
        .model-table td {
            padding: 6px 6px;
        }
        .col-num {
            width: 60px;
        }
    }
</style>
