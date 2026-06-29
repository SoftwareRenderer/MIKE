<script lang="ts">
    import ToggleSwitch from '../ToggleSwitch.svelte';

    let {
        defaultPlanMode = $bindable(false),
        defaultMergeOnCommit = $bindable(false),
        defaultMergeChildren = $bindable(true),
        defaultMaxFailedTurns = $bindable(3),
        originBranch,
    }: {
        defaultPlanMode?: boolean;
        defaultMergeOnCommit?: boolean;
        defaultMergeChildren?: boolean;
        defaultMaxFailedTurns?: number;
        originBranch: string;
    } = $props();
</script>

<fieldset class="section">
    <h3 class="section-label">Task Defaults</h3>
    <p class="section-description">These settings apply to newly created tasks</p>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-planmode-label" class="toggle-label">Plan mode</span>
            <span id="toggle-planmode-desc" class="toggle-desc">AI decomposes work into subtasks on the board before executing</span>
        </div>
        <ToggleSwitch bind:checked={defaultPlanMode} ariaLabel="Plan mode" ariaDescribedby="toggle-planmode-desc" />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergeoncommit-label" class="toggle-label">Auto-merge</span>
            <span id="toggle-mergeoncommit-desc" class="toggle-desc">Merge changes into <span class="branch-badge" title={`Branch: ${originBranch}`}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{originBranch}</span> when complete</span>
        </div>
        <ToggleSwitch bind:checked={defaultMergeOnCommit} ariaLabel="Auto-merge into main" ariaDescribedby="toggle-mergeoncommit-desc" />
    </div>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="toggle-mergechildren-label" class="toggle-label">Allow subtasks to auto-merge</span>
            <span id="toggle-mergechildren-desc" class="toggle-desc">Subtasks created under a task will auto-merge into it</span>
        </div>
        <ToggleSwitch bind:checked={defaultMergeChildren} ariaLabel="Allow subtasks to auto-merge" ariaDescribedby="toggle-mergechildren-desc" />
    </div>

    <div class="field-row">
        <div class="option-label-group">
            <label class="field-label-inline" for="default-max-failed">Max failed turns</label>
            <button type="button" class="help-icon" aria-label="Stop task after this many consecutive LLM failures">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                    <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                </svg>
            </button>
        </div>
        <input id="default-max-failed" type="number" bind:value={defaultMaxFailedTurns} min="1" max="20" class="option-input" aria-describedby="max-failed-help" />
        <span id="max-failed-help" class="sr-only">Stop task after this many consecutive LLM failures</span>
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
    .field-row {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 0;
    }
    .option-label-group {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .field-label-inline {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .help-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 16px;
        height: 16px;
        border-radius: 50%;
        background: var(--color-surface-3);
        border: none;
        color: var(--color-text-tertiary);
        cursor: help;
        font-size: 0.6875rem;
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
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border-width: 0;
    }
</style>
