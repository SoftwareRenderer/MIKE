<script lang="ts">
    
    import { isAgentProvider } from '$lib/types';
    import type { ProjectStats, ModelStats, Provider } from '$lib/types';
    import { getProjectStats, resetProjectStats } from '$lib/api';
    import { uiStore, appStore, dialogStore } from '$lib/stores';
    import { getProviderDisplayName } from '$lib/utils/providers';
    import { formatCost } from '$lib/utils/format';
    import { t } from '$lib/i18n';
    import { activeLocale } from '$lib/i18n/runtime.svelte';
    import BaseModal from './BaseModal.svelte';

    let { projectId, onClose }: { projectId: string; onClose: () => void } = $props();

    let stats: ProjectStats | null = $state(null);
    let loading = $state(true);
    let error = $state('');

    $effect(() => {
        async function load() {
            try {
                const data = await getProjectStats(projectId);
                stats = data;
            } catch (e) {
                error = String(e);
            } finally {
                loading = false;
            }
        }
        load();
    });

    function handleClose() {
        uiStore.showStats = false;
        onClose();
    }

    function formatNumber(n: number): string {
        return n.toLocaleString(activeLocale());
    }

    function formatTps(tps: number): string {
        if (tps === 0) return '-';
        if (tps >= 1000) {
            return `${(tps / 1000).toFixed(1)}k t/s`;
        }
        return `${Math.round(tps)} t/s`;
    }

    function formatKwh(kwh: number | undefined): string {
        if (!kwh || kwh === 0) return '-';
        if (kwh >= 1) return `${kwh.toFixed(3)} kWh`;
        // Show Wh for small values
        const wh = kwh * 1000;
        if (wh >= 1) return `${wh.toFixed(1)} Wh`;
        return `${(wh * 1000).toFixed(0)} mWh`;
    }

    /** Resolve a provider reference (LLM ID or agent name) to its display name. */
    function resolveProviderName(ref: string): string {
        const providers = appStore.config?.providers || [];
        const p = providers.find((c: Provider) =>
            isAgentProvider(c) ? c.name === ref : String(c.id) === ref || c.name === ref
        );
        return p ? getProviderDisplayName(p, providers) : ref;
    }

    function getTokenLabel(model: ModelStats): string {
        if (model.provider_name) {
            return `${resolveProviderName(model.provider_name)} / ${model.model_name}`;
        }
        return model.model_name;
    }

    // Sort model rows by generated tokens descending, filter out zeroed entries
    let sortedModels: ModelStats[] = $derived.by(() => {
        const models = (stats as ProjectStats | null)?.by_model || [];
        return models.filter((m: ModelStats) => m.task_count > 0 || m.generated_tokens > 0 || m.processed_tokens > 0 || m.cache_tokens > 0)
            .toSorted((a: ModelStats, b: ModelStats) => b.generated_tokens - a.generated_tokens);
    });

    async function resetModel(model: ModelStats) {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.stats.resetTitle'),
            message: t('settings.stats.resetConfirm', { model: getTokenLabel(model) }),
            confirmText: t('settings.stats.reset'),
            cancelText: t('app.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;
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

<BaseModal title={t('settings.stats.title')} width="90vw" maxWidth="860px" maxHeight="80dvh" onClose={handleClose}>
            {#if loading}
                <div class="loading">{t('settings.stats.loading')}</div>
            {:else if error}
                <div class="error">{error}</div>
            {:else if !stats || sortedModels.length === 0}
                <div class="empty">{t('settings.stats.empty')}</div>
            {:else}
                <!-- Overall summary -->
                <div class="summary-card">
                    <div class="summary-row">
                        <div class="summary-item">
                            <span class="summary-value">{stats.total_tasks}</span>
                            <span class="summary-label">{t('settings.stats.tasks')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_cache_tokens)}</span>
                            <span class="summary-label">{t('settings.stats.cached')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_processed_tokens)}</span>
                            <span class="summary-label">{t('settings.stats.prompt')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatNumber(stats.total_generated_tokens)}</span>
                            <span class="summary-label">{t('settings.stats.generated')}</span>
                        </div>
                    </div>
                    <div class="summary-row timings">
                        <div class="summary-item">
                            <span class="summary-value">{formatTps(stats.avg_prompt_tps)}</span>
                            <span class="summary-label">{t('settings.stats.promptSpeed')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatTps(stats.avg_generation_tps)}</span>
                            <span class="summary-label">{t('settings.stats.genSpeed')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatKwh(stats.total_power_kwh)}</span>
                            <span class="summary-label">{t('settings.stats.power')}</span>
                        </div>
                        <div class="summary-item">
                            <span class="summary-value">{formatCost(stats.total_cost, '-')}</span>
                            <span class="summary-label" title={t('settings.stats.costHint')}>{t('settings.stats.estCost')}</span>
                        </div>
                    </div>
                </div>

                <!-- Per-model breakdown -->
                <table class="model-table">
                    <thead>
                        <tr>
                            <th class="col-model">{t('settings.stats.model')}</th>
                            <th class="col-num">{t('settings.stats.tasks')}</th>
                            <th class="col-num">{t('settings.stats.cached')}</th>
                            <th class="col-num">{t('settings.stats.prompt')}</th>
                            <th class="col-num">{t('settings.stats.generated')}</th>
                            <th class="col-num">{t('settings.stats.promptSpeed')}</th>
                            <th class="col-num">{t('settings.stats.genSpeed')}</th>
                            <th class="col-num">{t('settings.stats.power')}</th>
                            <th class="col-num" title={t('settings.stats.costHint')}>{t('settings.stats.estCost')}</th>
                            <th class="col-action"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each sortedModels as model}
                            <tr>
                                <td class="col-model">
                                    <span class="model-label">{getTokenLabel(model)}</span>
                                </td>
                                <td class="col-num">{model.task_count}</td>
                                <td class="col-num">{formatNumber(model.cache_tokens)}</td>
                                <td class="col-num">{formatNumber(model.processed_tokens)}</td>
                                <td class="col-num">{formatNumber(model.generated_tokens)}</td>
                                <td class="col-num">{formatTps(model.avg_prompt_tps)}</td>
                                <td class="col-num">{formatTps(model.avg_generation_tps)}</td>
                                <td class="col-num">{formatKwh(model.power_kwh)}</td>
                                <td class="col-num">{formatCost(model.cost, '-')}</td>
                                <td class="col-action">
                                    <button class="btn-reset" onclick={() => resetModel(model)} title={t('settings.stats.resetHint')} aria-label={t('settings.stats.resetAria', { model: getTokenLabel(model) })}>
                                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <path d="M21 2v6h-6"/>
                                            <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                            <path d="M3 22v-6h6"/>
                                            <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                                        </svg>
                                        {t('settings.stats.reset')}
                                    </button>
                                </td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            {/if}
</BaseModal>

<style>
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
        font-size: var(--text-xl);
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        color: var(--color-text-primary);
    }
    .summary-label {
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    /* Model table */
    .model-table {
        width: 100%;
        border-collapse: collapse;
        font-size: var(--text-sm);
    }
    .model-table th {
        text-align: left;
        padding: 8px 10px;
        font-weight: 600;
        color: var(--color-text-secondary);
        border-bottom: 2px solid var(--color-border);
        font-size: var(--text-xs);
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
    .model-table th.col-num {
        text-align: right;
    }
    .col-action {
        text-align: center;
        width: 40px;
        padding: 8px 4px;
    }
    .model-table th.col-action {
        text-align: center;
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
        font-size: var(--text-xs);
        font-family: var(--font-sans);
        transition: color var(--dur-base) ease, background var(--dur-base) ease, border-color var(--dur-base) ease;
    }
    .btn-reset:hover {
        opacity: 1;
        color: var(--color-status-red);
        background: rgb(from var(--color-status-red) r g b / 0.1);
        border-color: rgb(from var(--color-status-red) r g b / 0.3);
    }
    .model-label {
        font-family: var(--font-mono, monospace);
        font-size: var(--text-sm);
    }
    @media (max-width: 768px) {
        .summary-row {
            flex-wrap: wrap;
        }
        .summary-item {
            min-width: 45%;
        }
        .model-table {
            font-size: var(--text-xs);
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
