<script lang="ts">
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';

    interface ModelEntry {
        name: string;
        readonly: boolean;
        reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
        context_window?: number | null;
        // Per-model pricing/power overrides — null = inherit provider default
        input_price?: number | null;
        output_price?: number | null;
        cache_read_price?: number | null;
        cache_write_price?: number | null;
        power_consumption_w?: number | null;
    }

    let { models, onAdd, onRemove, currencySymbol = '$', index = 0, onDirty }: {
        models: ModelEntry[];
        onAdd: () => void;
        onRemove: (modelIdx: number) => void;
        currencySymbol?: string;
        index?: number;
        onDirty?: () => void;
    } = $props();

    // Expanded rows (by model index).
    let expandedRows = $state<Set<number>>(new Set());

    function toggleExpand(modelIdx: number) {
        const next = new Set(expandedRows);
        if (next.has(modelIdx)) next.delete(modelIdx);
        else next.add(modelIdx);
        expandedRows = next;
    }

    // '' → null (inherit provider default)
    function parseFloatOrNull(v: string): number | null {
        if (v.trim() === '') return null;
        const n = parseFloat(v);
        return Number.isNaN(n) ? null : n;
    }

    // '' → null
    function parseIntOrNull(v: string): number | null {
        if (v.trim() === '') return null;
        const n = parseInt(v, 10);
        return Number.isNaN(n) ? null : n;
    }
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
        {#if models.length > 0}
            <div class="models-col-headers">
                <span class="col-model">Model</span>
                <span class="col-ctx">Context Window</span>
                <span class="col-reasoning">Reasoning Effort</span>
                <span class="col-expand" aria-hidden="true"></span>
                <span class="col-remove" aria-hidden="true"></span>
            </div>
        {/if}
        {#each models as model, mi}
            <div class="model-entry">
                <div class="model-row" class:readonly={model.readonly}>
                    <div class="model-name-cell">
                        {#if model.readonly}
                            <span class="model-name-label" title={model.name}>{model.name}</span>
                        {:else}
                            <input type="text" bind:value={model.name} placeholder="Model name" class="model-input" aria-label="Model name" oninput={() => onDirty?.()} />
                        {/if}
                    </div>
                    <div class="ctx-field" title="Context window in tokens. Pre-populated from Fetch Models; edit to override, or leave empty for auto-discovery.">
                        <input
                            type="number"
                            inputmode="numeric"
                            min="0"
                            step="any"
                            class="ctx-input"
                            placeholder="auto"
                            value={model.context_window ? String(model.context_window) : ''}
                            oninput={(e) => { model.context_window = parseIntOrNull((e.target as HTMLInputElement).value); onDirty?.(); }}
                            aria-label="Context window for {model.name || 'this model'}"
                        />
                    </div>
                    <select
                        class="reasoning-effort-select"
                        value={model.reasoning_effort || 'medium'}
                        oninput={(e) => { model.reasoning_effort = (e.target as HTMLSelectElement).value as 'off' | 'low' | 'medium' | 'high' | 'max'; onDirty?.(); }}
                        aria-label="Reasoning effort for {model.name || 'this model'}"
                        title="Reasoning effort (thinking budget)"
                    >
                        <optgroup label="Reasoning effort">
                            {#each REASONING_EFFORT_OPTIONS as option}
                                <option value={option.value}>{option.label}</option>
                            {/each}
                        </optgroup>
                    </select>
                    <button
                        type="button"
                        class="btn-expand-model"
                        onclick={() => toggleExpand(mi)}
                        aria-expanded={expandedRows.has(mi)}
                        aria-label={expandedRows.has(mi) ? `Collapse pricing for ${model.name || 'this model'}` : `Expand pricing for ${model.name || 'this model'}`}
                        title={expandedRows.has(mi) ? 'Collapse model pricing & power' : 'Model pricing & power'}
                    >
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" class:expanded={expandedRows.has(mi)}>
                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                    <button type="button" class="btn-remove-model" onclick={() => onRemove(mi)} aria-label={`Remove ${model.name || 'model'}`} title={`Remove ${model.name || 'model'}`}>
                        ✕
                    </button>
                </div>
                {#if expandedRows.has(mi)}
                    <div class="model-details">
                        <div class="model-details-header">
                            <span class="model-details-title">Model Pricing & Power</span>
                            <span class="model-details-hint">Leave empty to use the provider defaults below.</span>
                        </div>
                        <div class="model-price-grid">
                            <div class="mp-field">
                                <label for={`model-input-price-${index}-${mi}`}>Input</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-input-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.input_price ?? ''} oninput={(e) => { model.input_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-output-price-${index}-${mi}`}>Output</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-output-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.output_price ?? ''} oninput={(e) => { model.output_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-cache-read-price-${index}-${mi}`}>Cache Read</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-cache-read-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.cache_read_price ?? ''} oninput={(e) => { model.cache_read_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-cache-write-price-${index}-${mi}`}>Cache Write</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-cache-write-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.cache_write_price ?? ''} oninput={(e) => { model.cache_write_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-power-w-${index}-${mi}`}>Power (W)</label>
                                <input id={`model-power-w-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input mp-power-input" placeholder="default" value={model.power_consumption_w ?? ''} oninput={(e) => { model.power_consumption_w = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} title="Power draw override in watts. Empty = use provider default. Used for electricity cost estimates." />
                            </div>
                            <div class="mp-field mp-note-field">
                                <span class="mp-note">Prices are USD per 1M tokens. Cache Write replaces Input for non-cached tokens when set.</span>
                            </div>
                        </div>
                    </div>
                {/if}
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
    .models-col-headers,
    .model-row {
        display: grid;
        grid-template-columns: minmax(0, 1fr) 120px 150px 22px 22px;
        gap: 8px;
        align-items: center;
    }

    @media (max-width: 600px) {
        .models-col-headers,
        .model-row {
            grid-template-columns: minmax(100px, 1fr) 70px 90px 22px 22px;
        }
    }
    .models-col-headers {
        padding: 0 8px;
        border: 1px solid transparent;
        margin-bottom: 2px;
        font-size: 0.625rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .models-col-headers .col-ctx {
        text-align: right;
    }
    .model-entry {
        border-radius: var(--radius-sm);
    }
    .model-row {
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        border: 1px solid transparent;
    }
    .model-entry:has(.model-details) .model-row {
        border-bottom-left-radius: 0;
        border-bottom-right-radius: 0;
        border-bottom-color: var(--color-border);
    }
    .model-row.readonly {
        opacity: 0.85;
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
    .ctx-field {
        display: flex;
        align-items: center;
        width: 100%;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
        cursor: help;
    }
    .ctx-field:focus-within {
        border-color: var(--color-accent);
    }
    .ctx-input {
        flex: 1;
        min-width: 0;
        font-size: 0.75rem;
        padding: 2px 5px;
        background: transparent;
        border: none;
        color: var(--color-text-primary);
        text-align: right;
    }
    .ctx-input:focus {
        outline: none;
    }
    .ctx-input::placeholder {
        color: var(--color-text-tertiary);
        font-style: italic;
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
    .btn-expand-model {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        cursor: pointer;
        flex-shrink: 0;
        padding: 0;
    }
    .btn-expand-model:hover {
        background: var(--color-surface-1);
    }
    .btn-expand-model svg.expanded {
        transform: rotate(180deg);
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
    .model-details {
        padding: 8px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-top: none;
        border-radius: 0 0 var(--radius-sm) var(--radius-sm);
    }
    .model-details-header {
        display: flex;
        align-items: baseline;
        gap: 8px;
        margin-bottom: 6px;
    }
    .model-details-title {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .model-details-hint {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
    }
    .model-price-grid {
        display: grid;
        grid-template-columns: repeat(5, 1fr);
        gap: 6px;
        align-items: end;
    }

    @media (max-width: 600px) {
        .model-price-grid {
            grid-template-columns: repeat(2, 1fr);
        }
    }
    .mp-field {
        display: flex;
        flex-direction: column;
        gap: 3px;
        min-width: 0;
    }
    .mp-field label {
        font-size: 0.6875rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .mp-input-wrap {
        display: flex;
        align-items: center;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
    }
    .mp-input-wrap:focus-within {
        border-color: var(--color-accent);
    }
    .mp-prefix {
        padding: 2px 4px;
        font-size: 0.6875rem;
        color: var(--color-text-secondary);
        background: var(--color-surface-3);
        border-right: 1px solid var(--color-border);
        white-space: nowrap;
        user-select: none;
    }
    .mp-input {
        flex: 1;
        min-width: 0;
        font-size: 0.75rem;
        padding: 2px 5px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        width: 100%;
    }
    .mp-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .mp-input::placeholder {
        color: var(--color-text-tertiary);
        font-style: italic;
    }
    .mp-note-field {
        grid-column: 1 / -1;
    }
    .mp-note {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
    }
    .models-empty {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        text-align: center;
        padding: 12px;
        font-style: italic;
    }
</style>
