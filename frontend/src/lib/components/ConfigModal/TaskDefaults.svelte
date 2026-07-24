<script lang="ts">
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ModeSelect from '../ModeSelect.svelte';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';

    let {
        defaultOperatingMode = $bindable('act'),
        defaultAutoMerge = $bindable(true),
        defaultSubtasksAutoMerge = $bindable(true),
        defaultMaxFailedTurns = $bindable(3),
        originBranch,
        onDirty,
    }: {
        defaultOperatingMode?: string;
        defaultAutoMerge?: boolean;
        defaultSubtasksAutoMerge?: boolean;
        defaultMaxFailedTurns?: number;
        originBranch: string;
        onDirty?: () => void;
    } = $props();

    const modeDescription = $derived(MODE_DESCRIPTIONS[defaultOperatingMode] || '');
</script>

<fieldset class="section">
    <h3 class="section-label">Task Defaults</h3>
    <p class="section-description">These settings apply to newly created tasks</p>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mode-label" class="toggle-label">Operating mode</span>
            <span id="toggle-mode-desc" class="toggle-desc">{modeDescription}</span>
        </div>
        <ModeSelect value={defaultOperatingMode} onChange={(m: string) => { defaultOperatingMode = m; onDirty?.(); }} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergeoncommit-label" class="toggle-label">Auto-merge</span>
            <span id="toggle-mergeoncommit-desc" class="toggle-desc">Merge changes into <span class="branch-badge" title={`Branch: ${originBranch}`}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{originBranch}</span> when complete</span>
        </div>
        <ToggleSwitch bind:checked={defaultAutoMerge} ariaLabel="Auto-merge into main" ariaDescribedby="toggle-mergeoncommit-desc" onChange={() => onDirty?.()} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergechildren-label" class="toggle-label">Allow subtasks to auto-merge</span>
            <span id="toggle-mergechildren-desc" class="toggle-desc">Subtasks created under a task will auto-merge into it</span>
        </div>
        <ToggleSwitch bind:checked={defaultSubtasksAutoMerge} ariaLabel="Allow subtasks to auto-merge" ariaDescribedby="toggle-mergechildren-desc" onChange={() => onDirty?.()} />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <label class="toggle-label" for="default-max-failed">Max failed turns</label>
            <span class="toggle-desc">Stop task after this many consecutive LLM failures</span>
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
</style>
