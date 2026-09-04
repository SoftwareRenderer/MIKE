<script lang="ts">
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';
    import { t } from '$lib/i18n';

    interface ModelEntry {
        name: string;
        readonly: boolean;
        reasoning_effort?: string; // known level or custom value (passed through to the server)
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

    // Reasoning-effort suggestion list: which model row's list is open + active option.
    // effortQuery is the text typed since focus — the pre-filled value does not
    // filter the list shown on focus (matches native datalist behavior).
    let effortOpen = $state(-1);
    let effortActive = $state(0);
    let effortQuery = $state('');

    function closeEffortList() {
        effortOpen = -1;
    }

    // Suggestion options filtered by the typed text (empty = all).
    function visibleEffortOptions(): Array<{ value: string; label: string }> {
        const q = effortQuery.trim().toLowerCase();
        if (!q) return REASONING_EFFORT_OPTIONS;
        return REASONING_EFFORT_OPTIONS.filter(o => o.value.toLowerCase().includes(q));
    }

    function selectEffort(mi: number, value: string) {
        models[mi].reasoning_effort = value;
        effortOpen = -1;
        onDirty?.();
    }

    function handleEffortKeydown(e: KeyboardEvent, mi: number) {
        const opts = visibleEffortOptions();
        const n = opts.length;
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            if (n === 0) return;
            if (effortOpen !== mi) {
                effortOpen = mi;
                effortActive = 0;
                return;
            }
            effortActive = e.key === 'ArrowDown'
                ? (effortActive + 1) % n
                : (effortActive - 1 + n) % n;
        } else if (e.key === 'Enter') {
            if (effortOpen === mi && n > 0 && (models[mi].reasoning_effort ?? '').trim() !== opts[effortActive].value) {
                e.preventDefault();
                selectEffort(mi, opts[effortActive].value);
            }
        } else if (e.key === 'Escape') {
            closeEffortList();
        }
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
        <span class="models-label">{t('settings.models.title')} <span class="model-count">{models.filter(m => m.name.trim()).length}</span></span>
        <button type="button" class="btn-add-model" onclick={onAdd} title={t('settings.models.addModelHint')}>
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                <path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
            {t('settings.models.addModel')}
        </button>
    </div>
    <div class="models-list">
        {#if models.length > 0}
            <div class="models-col-headers">
                <span class="col-model">{t('settings.models.colModel')}</span>
                <span class="col-ctx">{t('settings.models.colContext')}</span>
                <span class="col-reasoning">{t('settings.models.colEffort')}</span>
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
                            <input type="text" bind:value={model.name} placeholder={t('settings.models.nameField')} class="model-input" aria-label={t('settings.models.nameField')} oninput={() => onDirty?.()} />
                        {/if}
                    </div>
                    <div class="ctx-field" title={t('settings.models.ctxHint')}>
                        <input
                            type="number"
                            inputmode="numeric"
                            min="0"
                            step="any"
                            class="ctx-input"
                            placeholder={t('settings.models.auto')}
                            value={model.context_window ? String(model.context_window) : ''}
                            oninput={(e) => { model.context_window = parseIntOrNull((e.target as HTMLInputElement).value); onDirty?.(); }}
                            aria-label={t('settings.models.ctxAria', { model: model.name || t('settings.models.thisModel') })}
                        />
                    </div>
                    <div class="reasoning-effort-field">
                        <input
                            type="text"
                            class="reasoning-effort-input"
                            value={model.reasoning_effort ?? 'default'}
                            onfocus={() => { effortOpen = mi; effortActive = 0; effortQuery = ''; }}
                            oninput={(e) => { model.reasoning_effort = (e.target as HTMLInputElement).value; effortActive = 0; effortQuery = (e.target as HTMLInputElement).value; onDirty?.(); }}
                            onblur={closeEffortList}
                            onkeydown={(e) => handleEffortKeydown(e, mi)}
                            placeholder="default"
                            role="combobox"
                            aria-expanded={effortOpen === mi && visibleEffortOptions().length > 0}
                            aria-controls={`reasoning-effort-list-${index}-${mi}`}
                            aria-activedescendant={effortOpen === mi ? `reasoning-effort-opt-${index}-${mi}-${Math.min(effortActive, Math.max(visibleEffortOptions().length - 1, 0))}` : undefined}
                            aria-label={t('settings.models.effortAria', { model: model.name || t('settings.models.thisModel') })}
                            title={t('settings.models.effortHint')}
                        />
                        {#if effortOpen === mi && visibleEffortOptions().length > 0}
                            <div class="reasoning-effort-list" role="listbox" id={`reasoning-effort-list-${index}-${mi}`}>
                                {#each visibleEffortOptions() as opt, oi}
                                    <div
                                        id={`reasoning-effort-opt-${index}-${mi}-${oi}`}
                                        class="reasoning-effort-option"
                                        class:active={oi === effortActive}
                                        role="option"
                                        tabindex="-1"
                                        aria-selected={oi === effortActive}
                                        onmousedown={(e) => { e.preventDefault(); selectEffort(mi, opt.value); }}
                                    >{opt.label}</div>
                                {/each}
                            </div>
                        {/if}
                    </div>
                    <button
                        type="button"
                        class="btn-expand-model"
                        onclick={() => toggleExpand(mi)}
                        aria-expanded={expandedRows.has(mi)}
                        aria-label={expandedRows.has(mi) ? t('settings.models.collapsePricingAria', { model: model.name || t('settings.models.thisModel') }) : t('settings.models.expandPricingAria', { model: model.name || t('settings.models.thisModel') })}
                        title={expandedRows.has(mi) ? t('settings.models.collapsePricingTitle') : t('settings.models.expandPricingTitle')}
                    >
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" class:expanded={expandedRows.has(mi)}>
                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                    <button type="button" class="btn-remove-model" onclick={() => onRemove(mi)} aria-label={t('settings.models.removeAria', { model: model.name || t('settings.models.modelWord') })} title={t('settings.models.removeAria', { model: model.name || t('settings.models.modelWord') })}>
                        ✕
                    </button>
                </div>
                {#if expandedRows.has(mi)}
                    <div class="model-details">
                        <div class="model-details-header">
                            <span class="model-details-title">{t('settings.models.detailsTitle')}</span>
                            <span class="model-details-hint">{t('settings.models.detailsHint')}</span>
                        </div>
                        <div class="model-price-grid">
                            <div class="mp-field">
                                <label for={`model-input-price-${index}-${mi}`}>{t('settings.pricing.input')}</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-input-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.input_price ?? ''} oninput={(e) => { model.input_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-output-price-${index}-${mi}`}>{t('settings.pricing.output')}</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-output-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.output_price ?? ''} oninput={(e) => { model.output_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-cache-read-price-${index}-${mi}`}>{t('settings.pricing.cacheRead')}</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-cache-read-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.cache_read_price ?? ''} oninput={(e) => { model.cache_read_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-cache-write-price-${index}-${mi}`}>{t('settings.pricing.cacheWrite')}</label>
                                <div class="mp-input-wrap">
                                    <span class="mp-prefix">{currencySymbol}</span>
                                    <input id={`model-cache-write-price-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input" placeholder="default" value={model.cache_write_price ?? ''} oninput={(e) => { model.cache_write_price = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} />
                                </div>
                            </div>
                            <div class="mp-field">
                                <label for={`model-power-w-${index}-${mi}`}>{t('settings.models.power')}</label>
                                <input id={`model-power-w-${index}-${mi}`} type="number" inputmode="decimal" min="0" step="any" class="mp-input mp-power-input" placeholder="default" value={model.power_consumption_w ?? ''} oninput={(e) => { model.power_consumption_w = parseFloatOrNull((e.target as HTMLInputElement).value); onDirty?.(); }} title={t('settings.models.powerHint')} />
                            </div>
                            <div class="mp-field mp-note-field">
                                <span class="mp-note">{t('settings.models.note')}</span>
                            </div>
                        </div>
                    </div>
                {/if}
            </div>
        {/each}
    </div>
    {#if models.length === 0}
        <div class="models-empty">{t('settings.models.empty')}</div>
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
    .reasoning-effort-field {
        position: relative;
        width: 100%;
        flex-shrink: 0;
    }
    .reasoning-effort-input {
        width: 100%;
        font-size: 0.75rem;
        padding: 2px 4px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
    }
    .reasoning-effort-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .reasoning-effort-input::placeholder {
        color: var(--color-text-tertiary);
        font-style: italic;
    }
    .reasoning-effort-list {
        position: absolute;
        top: calc(100% + 2px);
        left: 0;
        right: 0;
        z-index: 30;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
        overflow: hidden;
    }
    .reasoning-effort-option {
        font-size: 0.75rem;
        line-height: 1.2;
        padding: 4px 8px;
        color: var(--color-text-secondary);
        cursor: pointer;
        user-select: none;
        white-space: nowrap;
    }
    .reasoning-effort-option:hover,
    .reasoning-effort-option.active {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
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
