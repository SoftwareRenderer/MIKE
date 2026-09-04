<script lang="ts">
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ModeSelect from '../ModeSelect.svelte';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';
    import { t } from '$lib/i18n';

    let {
        defaultOperatingMode = $bindable('act'),
        defaultAutoMerge = $bindable(true),
        defaultSubtasksAutoMerge = $bindable(true),
        defaultMaxFailedTurns = $bindable(3),
        defaultAutoSummarizeThreshold = $bindable(90),
        runSiblingsParallel = $bindable(false),
        originBranch,
        onDirty,
    }: {
        defaultOperatingMode?: string;
        defaultAutoMerge?: boolean;
        defaultSubtasksAutoMerge?: boolean;
        defaultMaxFailedTurns?: number;
        defaultAutoSummarizeThreshold?: number;
        runSiblingsParallel?: boolean;
        originBranch: string;
        onDirty?: () => void;
    } = $props();

    const modeDescription = $derived(MODE_DESCRIPTIONS[defaultOperatingMode] || '');
</script>

<fieldset class="section">
    <h3 class="section-label">{t('settings.tab.taskDefaults')}</h3>
    <p class="section-description">{t('settings.defaults.description')}</p>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mode-label" class="toggle-label">{t('settings.defaults.operatingMode')}</span>
            <span id="toggle-mode-desc" class="toggle-desc">{modeDescription}</span>
        </div>
        <ModeSelect value={defaultOperatingMode} onChange={(m: string) => { defaultOperatingMode = m; onDirty?.(); }} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergeoncommit-label" class="toggle-label">{t('settings.defaults.autoMerge')}</span>
            <span id="toggle-mergeoncommit-desc" class="toggle-desc">{t('settings.defaults.autoMergeDesc.before')} <span class="branch-badge" title={t('settings.defaults.branchTitle', { branch: originBranch })}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{originBranch}</span> {t('settings.defaults.autoMergeDesc.after')}</span>
        </div>
        <ToggleSwitch bind:checked={defaultAutoMerge} ariaLabel={t('settings.defaults.autoMergeAria')} ariaDescribedby="toggle-mergeoncommit-desc" onChange={() => onDirty?.()} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergechildren-label" class="toggle-label">{t('settings.defaults.subtasksAutoMerge')}</span>
            <span id="toggle-mergechildren-desc" class="toggle-desc">{t('settings.defaults.subtasksAutoMergeDesc')}</span>
        </div>
        <ToggleSwitch bind:checked={defaultSubtasksAutoMerge} ariaLabel={t('settings.defaults.subtasksAutoMerge')} ariaDescribedby="toggle-mergechildren-desc" onChange={() => onDirty?.()} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <label class="toggle-label" for="default-summarize-threshold">{t('settings.defaults.autoSummarize')}</label>
            <span class="toggle-desc">{t('settings.defaults.autoSummarizeDesc')}</span>
        </div>
        <div class="percent-input-wrap">
            <input id="default-summarize-threshold" type="number" bind:value={defaultAutoSummarizeThreshold} min="1" max="100" class="option-input" onchange={() => onDirty?.()} />
            <span class="percent-suffix">%</span>
        </div>
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-run-parallel-label" class="toggle-label">{t('settings.defaults.runParallel')}</span>
            <span id="toggle-run-parallel-desc" class="toggle-desc">{t('settings.defaults.runParallelDesc')}</span>
        </div>
        <ToggleSwitch bind:checked={runSiblingsParallel} ariaLabel={t('settings.defaults.runParallel')} ariaDescribedby="toggle-run-parallel-desc" onChange={() => onDirty?.()} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <label class="toggle-label" for="default-max-failed">{t('settings.defaults.maxFailed')}</label>
            <span class="toggle-desc">{t('settings.defaults.maxFailedDesc')}</span>
        </div>
        <input id="default-max-failed" type="number" bind:value={defaultMaxFailedTurns} min="1" max="20" class="option-input" onchange={() => onDirty?.()} />
    </div>
</fieldset>

<style>
    .section {
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
        margin-bottom: 16px;
    }
    .section-label {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0 0 4px;
        color: var(--color-text-primary);
    }
    .section-description {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
        margin: 0 0 12px;
    }
    .toggle-field {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
        gap: 12px;
    }
    .toggle-label-group {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
        flex: 1;
    }
    .toggle-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .toggle-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        line-height: 1.4;
    }
    .branch-badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        padding: 1px 6px;
        background: color-mix(in srgb, var(--color-accent) 15%, transparent);
        border-radius: 4px;
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-accent);
    }
    .option-input {
        width: 60px;
        padding: 4px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
    }
    .percent-input-wrap {
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .percent-suffix {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
    }
</style>
