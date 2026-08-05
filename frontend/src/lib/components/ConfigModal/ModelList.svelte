<script lang="ts">
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';

    interface ModelEntry {
        name: string;
        readonly: boolean;
        reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
        context_window?: number;
    }

    let { models, onAdd, onRemove }: {
        models: ModelEntry[];
        onAdd: () => void;
        onRemove: (modelIdx: number) => void;
    } = $props();
</script>

<div class="models-section">
    <div class="models-header">
        <span class="models-label">MODELS <span class="model-count">{models.filter(m => m.name.trim()).length}</span></span>
        <button type="button" class="btn-add-model" onclick={onAdd} title="Add a model manually">
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                <path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
            Add Model
        </button>
    </div>
    <div class="models-list">
        {#each models as model, mi}
            <div class="model-row" class:readonly={model.readonly}>
                <div class="model-name-cell">
                    {#if model.readonly}
                        <span class="model-name-label">{model.name}</span>
                    {:else}
                        <input type="text" bind:value={model.name} placeholder="Model name" class="model-input" aria-label="Model name" />
                    {/if}
                    {#if model.context_window}
                        <span class="context-window-badge" title="Context window: {model.context_window} tokens">
                            {(model.context_window >= 1_000_000 ? (model.context_window / 1_000_000).toFixed(1) + 'M' : (model.context_window / 1_000).toFixed(0) + 'K')} context
                        </span>
                    {/if}
                </div>
                <select
                    class="reasoning-effort-select"
                    value={model.reasoning_effort || 'medium'}
                    oninput={(e) => { model.reasoning_effort = (e.target as HTMLSelectElement).value as 'off' | 'low' | 'medium' | 'high' | 'max'; }}
                    aria-label="Reasoning effort for {model.name || 'this model'}"
                    title="Reasoning effort (thinking budget)"
                >
                    <optgroup label="Reasoning effort">
                        {#each REASONING_EFFORT_OPTIONS as option}
                            <option value={option.value}>{option.label}</option>
                        {/each}
                    </optgroup>
                </select>
                <button type="button" class="btn-remove-model" onclick={() => onRemove(mi)} aria-label={`Remove ${model.name || 'model'}`} title={`Remove ${model.name || 'model'}`}>
                    ✕
                </button>
            </div>
        {/each}
    </div>
    {#if models.length === 0}
        <div class="models-empty">No models added. Use "Fetch Models" or add manually.</div>
    {/if}
</div>

<style>
    .models-section {
        margin-top: 12px;
    }
    .models-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 8px;
    }
    .models-label {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .model-count {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        min-width: 18px;
        height: 18px;
        padding: 0 4px;
        border-radius: 9px;
        background: var(--color-surface-3);
        color: var(--color-text-tertiary);
        font-size: 0.625rem;
        font-weight: 500;
    }
    .btn-add-model {
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        color: var(--color-accent);
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
    }
    .btn-add-model:hover {
        opacity: 0.8;
    }
    .models-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .model-row {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        border: 1px solid transparent;
    }
    .model-row.readonly {
        opacity: 0.7;
    }
    .model-name-cell {
        display: flex;
        align-items: center;
        gap: 6px;
        flex: 1;
        min-width: 0;
    }
    .model-name-label {
        font-size: 0.8125rem;
        font-weight: 500;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .model-input {
        flex: 1;
        min-width: 0;
        font-size: 0.8125rem;
        padding: 2px 6px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
    }
    .model-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .context-window-badge {
        font-size: 0.6875rem;
        color: var(--color-text-secondary);
        background: var(--color-surface-1);
        padding: 1px 6px;
        border-radius: var(--radius-sm);
        white-space: nowrap;
        flex-shrink: 0;
    }
    .reasoning-effort-select {
        font-size: 0.75rem;
        padding: 2px 4px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        cursor: pointer;
        flex-shrink: 0;
    }
    .reasoning-effort-select:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .btn-remove-model {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        border-radius: var(--radius-sm);
        flex-shrink: 0;
    }
    .btn-remove-model:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    .models-empty {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        text-align: center;
        padding: 12px;
        font-style: italic;
    }
</style>
