<script lang="ts">
    import { onMount } from 'svelte';
    import type { Config, Provider } from '$lib/types';
    import { updateConfig, getConfig, fetchModels as fetchModelsApi, testConnection as testConnectionApi } from '$lib/api';
    import { config as configStore, showConfig } from '$lib/store';
    import { createFocusTrap } from '$lib/a11y';
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';

    let { config, onClose, onSaved }: { config: Config | null; onClose: () => void; onSaved: () => void } = $props();

    const MODEL_COLORS = [
        '#6E7681', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
        '#A371F7', '#E3B341', '#79C0FF', '#7EE787', '#FF7B72',
        '#D2A8FF', '#F0883E', '#56B6C2', '#F778BA', '#DB6D28',
    ];
    function getModelColor(modelName: string): string {
        let hash = 0;
        for (let i = 0; i < modelName.length; i++) {
            hash = ((hash << 5) - hash + modelName.charCodeAt(i)) | 0;
        }
        return MODEL_COLORS[Math.abs(hash) % MODEL_COLORS.length];
    }

    let workspaceDir = $state(config?.workspace_dir || '');
    let defaultProvider = $state(config?.default_provider || '');
    let defaultAutoCommit = $state(config?.default_auto_commit ?? false);
    let defaultPlanMode = $state(config?.default_plan_mode ?? false);
    let defaultMaxFailedTurns = $state(config?.default_max_failed_turns ?? 3);
    let loading = $state(false);
    let error = $state('');

    // Locale-aware currency formatting for pricing inputs
    const currencySymbol = (() => {
        const parts = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD', minimumFractionDigits: 0, maximumFractionDigits: 0 }).formatToParts(0);
        const currencyPart = parts.find(p => p.type === 'currency');
        return currencyPart ? currencyPart.value : '$';
    })();

    /** Format a price number as a locale-aware string with 2 decimal places. */
    function formatPrice(value: number): string {
        if (!value && value !== 0) return '';
        return Number(value).toLocaleString(undefined, {
            minimumFractionDigits: 2,
            maximumFractionDigits: 4,
        });
    }

    /** Parse a formatted price string back to a number. */
    function parsePrice(str: string): number {
        const cleaned = str.replace(/[^0-9.]/g, '');
        const n = parseFloat(cleaned);
        return isNaN(n) ? 0 : n;
    }

    // -- Provider management --
    interface ModelEntry {
        name: string;
        readonly: boolean; // true = fetched from API, not editable
        reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
    }

    interface ProviderDraft {
        name: string;
        display_name: string;
        url: string;
        models: ModelEntry[];
        concurrency: number;
        default_model: string; // per-provider default model
        // Pricing (USD per 1M tokens)
        input_price: number;
        output_price: number;
        cache_read_price: number;
        cache_write_price: number;
        fetchingModels: boolean;
        fetchError: string;
        expanded: boolean; // card expansion state
        testingConnection: boolean;
        testResult: { success: boolean; message: string } | null;
    }

    /** Generate a provider name from a URL (hostname-port slug). */
    function generateProviderName(url: string): string {
        try {
            const u = new URL(url);
            const host = u.hostname.replace(/[^a-z0-9]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
            const port = u.port || (u.protocol === 'https:' ? '443' : '80');
            return `${host}-${port}`;
        } catch {
            return `provider-${Date.now()}`;
        }
    }

    /** Generate a display name from a URL (TitleCase hostname:port). */
    function generateDisplayName(url: string): string {
        try {
            const u = new URL(url);
            const host = u.hostname.charAt(0).toUpperCase() + u.hostname.slice(1);
            const port = u.port || (u.protocol === 'https:' ? '443' : '80');
            return `${host}:${port}`;
        } catch {
            return 'Provider';
        }
    }

    /** Derive name and display_name from URL for a provider draft. */
    function syncProviderMeta(p: ProviderDraft) {
        if (p.url.trim()) {
            p.name = generateProviderName(p.url.trim());
            p.display_name = generateDisplayName(p.url.trim());
        } else {
            p.name = '';
            p.display_name = '';
        }
    }

    // Migrate old global default_model to the matching provider's default_model
    const oldGlobalDefault = config?.default_model || '';

    let providers = $state<ProviderDraft[]>(
        (config?.providers || []).map(p => {
            const draft: ProviderDraft = {
                ...p,
                concurrency: p.concurrency || 1,
                default_model: p.default_model || '',
                input_price: p.input_price || 0,
                output_price: p.output_price || 0,
                cache_read_price: p.cache_read_price || 0,
                cache_write_price: p.cache_write_price || 0,
                fetchingModels: false,
                fetchError: '',
                expanded: !p.default_model, // collapse if already configured
                testingConnection: false,
                testResult: null,
                models: (p.models || []).map(m => {
                    // Handle both old format (string) and new format ({name, reasoning_effort})
                    if (typeof m === 'string') {
                        return { name: m, readonly: false, reasoning_effort: 'medium' as const };
                    }
                    return { name: m.name, readonly: false, reasoning_effort: m.reasoning_effort || 'medium' };
                }).sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })),
            };
            // Migrate: if old global default_model belongs to this provider and provider has no default set
            if (!draft.default_model && oldGlobalDefault && p.models?.some(m => (typeof m === 'string' ? m : m.name) === oldGlobalDefault)) {
                draft.default_model = oldGlobalDefault;
            }
            return draft;
        })
    );

    function toggleProvider(idx: number) {
        providers[idx].expanded = !providers[idx].expanded;
    }

    function handleProviderHeaderKeydown(e: KeyboardEvent, idx: number) {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            toggleProvider(idx);
        }
    }

    async function testProviderConnection(idx: number) {
        const p = providers[idx];
        if (!p.url.trim()) return;
        p.testingConnection = true;
        p.testResult = null;
        try {
            const defaultModelName = p.default_model ? parseDefaultModel(p.default_model).name : (p.models.find(m => m.name)?.name || '');
            const result = await testConnectionApi(p.url.trim(), defaultModelName);
            p.testResult = {
                success: result.success,
                message: result.message,
            };
        } catch (e) {
            p.testResult = { success: false, message: String(e) };
        } finally {
            p.testingConnection = false;
        }
    }

    function addProvider() {
        providers.push({
            name: '',
            display_name: '',
            url: '',
            models: [],
            concurrency: 1,
            default_model: '',
            input_price: 0,
            output_price: 0,
            cache_read_price: 0,
            cache_write_price: 0,
            fetchingModels: false,
            fetchError: '',
            expanded: true,
            testingConnection: false,
            testResult: null,
        });
    }

    function removeProvider(idx: number) {
        providers.splice(idx, 1);
    }

    function swapProviders(fromIdx: number, toIdx: number) {
        if (fromIdx === toIdx || toIdx < 0 || toIdx >= providers.length) return;
        const [item] = providers.splice(fromIdx, 1);
        providers.splice(toIdx, 0, item);
    }

    // -- Drag-and-drop reordering --
    let dragIndex = $state(-1);
    let dragDropIndex = $state(-1);
    let dragPreview: HTMLElement | null = null;
    let providerCards: HTMLDivElement[] = $state([]);

    /** Find the provider card index under the given pointer coordinates. */
    function getCardIndexAt(x: number, y: number): number {
        const el = document.elementFromPoint(x, y);
        if (!el) return -1;
        const card = el.closest('.provider-card');
        if (!card) return -1;
        return parseInt(card.getAttribute('data-index') || '-1', 10);
    }

    function handleDragStart(e: PointerEvent, idx: number) {
        e.preventDefault();
        e.stopPropagation();
        if (providers.length < 2) return;

        dragIndex = idx;
        dragDropIndex = -1;

        // Create drag preview element
        dragPreview = document.createElement('div');
        dragPreview.className = 'drag-preview';
        dragPreview.style.left = `${e.clientX - 12}px`;
        dragPreview.style.top = `${e.clientY - 12}px`;
        document.body.appendChild(dragPreview);

        const startY = e.clientY;
        const startIdx = idx;

        // Mark card as dragging
        const card = providerCards[idx];
        if (card) card.classList.add('dragging');

        function onPointerMove(ev: PointerEvent) {
            ev.preventDefault();
            // Update preview position
            if (dragPreview) {
                dragPreview.style.left = `${ev.clientX - 12}px`;
                dragPreview.style.top = `${ev.clientY - 12}px`;
            }
            // Find drop target
            const targetIdx = getCardIndexAt(ev.clientX, ev.clientY);
            if (targetIdx >= 0 && targetIdx !== dragIndex && targetIdx !== dragDropIndex) {
                // Adjust index if dragging downwards and target is the old position
                let adjustedIdx = targetIdx;
                if (dragIndex < targetIdx && targetIdx !== startIdx) {
                    // target is below drag source — no adjustment needed
                }
                dragDropIndex = adjustedIdx;
                swapProviders(dragIndex, adjustedIdx);
                dragIndex = adjustedIdx;
            }
        }

        function onPointerUp(ev: PointerEvent) {
            ev.preventDefault();
            document.removeEventListener('pointermove', onPointerMove as any);
            document.removeEventListener('pointerup', onPointerUp as any);

            // Remove drag preview
            if (dragPreview) {
                dragPreview.remove();
                dragPreview = null;
            }
            // Clear dragging class from all cards
            for (const c of providerCards) c.classList.remove('dragging');
            dragIndex = -1;
            dragDropIndex = -1;
        }

        document.addEventListener('pointermove', onPointerMove, { passive: false });
        document.addEventListener('pointerup', onPointerUp);
    }

    async function fetchModelsForProvider(idx: number) {
        const p = providers[idx];
        if (!p.url.trim()) return;
        p.fetchingModels = true;
        p.fetchError = '';
        try {
            const result = await fetchModelsApi(p.url.trim());
            if (result.error) {
                p.fetchError = result.error;
            } else {
                // Replace: clear existing models and use fetched list (readonly)
                p.models = result.models.map(m => ({ name: m.name, readonly: true, reasoning_effort: 'medium' as const, context_window: m.context_window }));
                p.models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
            }
        } catch (e) {
            p.fetchError = String(e);
        } finally {
            p.fetchingModels = false;
        }
    }

    function addModelToProvider(idx: number) {
        providers[idx].models.push({ name: '', readonly: false, reasoning_effort: 'medium' });
        providers[idx].models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    }

    function removeModelFromProvider(idx: number, modelIdx: number) {
        const p = providers[idx];
        const removed = p.models[modelIdx];
        const removedName = removed?.name || '';
        const removedEffort = removed?.reasoning_effort || 'medium';
        const removedCompositeKey = removedEffort !== 'medium' ? `${removedName}|${removedEffort}` : removedName;
        p.models.splice(modelIdx, 1);
        // Clear default if the removed model+effort pair was the default
        if (p.default_model === removedCompositeKey) {
            p.default_model = '';
        }
    }

    function setProviderDefaultModel(idx: number, modelName: string, reasoningEffort?: string) {
        const p = providers[idx];
        const compositeKey = reasoningEffort && reasoningEffort !== 'medium' ? `${modelName}|${reasoningEffort}` : modelName;
        if (p.default_model === compositeKey) {
            p.default_model = ''; // toggle off
        } else {
            p.default_model = compositeKey;
        }
    }

    /** Parse a default_model value (possibly modelName|effort) into {name, effort}. */
    function parseDefaultModel(value: string): { name: string; effort: string } {
        if (!value) return { name: '', effort: '' };
        const parts = value.split('|');
        return { name: parts[0], effort: parts[1] || 'medium' };
    }

    function toggleDefaultProvider(providerName: string) {
        if (defaultProvider === providerName) {
            defaultProvider = '';
        } else {
            defaultProvider = providerName;
            // Move default provider to top
            const idx = providers.findIndex(p => p.name === providerName);
            if (idx > 0) {
                const [item] = providers.splice(idx, 1);
                providers.unshift(item);
            }
        }
    }

    async function handleSave() {
        loading = true;
        error = '';
        try {
            const savedProviders: Provider[] = providers
                .filter(p => p.url.trim())
                .map(p => {
                    syncProviderMeta(p);
                    return {
                        name: p.name.trim(),
                        display_name: p.display_name.trim(),
                        url: p.url.trim(),
                        models: p.models.filter(m => m.name.trim()).map(m => ({
                            name: m.name.trim(),
                            reasoning_effort: m.reasoning_effort || 'medium',
                        })),
                        concurrency: Math.max(1, p.concurrency || 1),
                        default_model: p.default_model || '',
                        input_price: p.input_price,
                        output_price: p.output_price,
                        cache_read_price: p.cache_read_price,
                        cache_write_price: p.cache_write_price,
                    };
                });
            const cfg = await updateConfig({
                workspace_dir: workspaceDir.trim(),
                providers: savedProviders,
                default_provider: defaultProvider,
                default_auto_commit: defaultAutoCommit,
                default_plan_mode: defaultPlanMode,
                default_max_failed_turns: defaultMaxFailedTurns,
            });
            configStore.set(cfg);
            loading = false;
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    let modalEl: HTMLDivElement | null = null;
    let focusTrap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;

    onMount(() => {
        // Store the element that triggered this modal for focus restoration
        triggerEl = document.activeElement as HTMLElement;
        // Setup focus trap after modal is rendered
        if (modalEl) {
            focusTrap = createFocusTrap(modalEl, handleClose);
            // Focus the close button or first interactive element
            setTimeout(() => focusTrap?.focusFirst(), 50);
        }
    });

    function handleClose() {
        focusTrap?.destroy();
        focusTrap = null;
        onClose();
        // Restore focus to trigger element
        setTimeout(() => triggerEl?.focus(), 50);
    }
</script>

<div class="overlay" onclick={handleClose} role="presentation">
    <div class="modal" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={modalEl} role="dialog" aria-modal="true" aria-labelledby="config-modal-title" tabindex="-1">
        <div class="modal-header">
            <h2 id="config-modal-title">Settings</h2>
            <button class="close-btn" onclick={handleClose} aria-label="Close settings" title="Close settings">✕</button>
        </div>
        <form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
            <!-- Providers Section -->
            <fieldset class="section">
                <div class="section-header">
                    <h3 class="section-label">Providers</h3>
                    <button type="button" class="btn-small" onclick={addProvider} title="Add a new LLM provider">+ Add Provider</button>
                </div>
                <div class="providers-list">
                    {#each providers as p, i}
                        <div class="provider-card" data-index={i} bind:this={providerCards[i]} class:dragging={dragIndex === i}>
                            <!-- Card header: always visible -->
                            <div class="provider-card-header" onclick={() => toggleProvider(i)} aria-expanded={p.expanded}>
                                <div class="reorder-buttons">
                                    <button type="button" class="btn-drag-handle" onpointerdown={(e) => handleDragStart(e, i)} title="Drag to reorder" aria-label="Drag to reorder provider">
                                        <svg width="16" height="16" viewBox="0 0 16 20" fill="currentColor">
                                            <circle cx="5" cy="3" r="1.5"/>
                                            <circle cx="11" cy="3" r="1.5"/>
                                            <circle cx="5" cy="10" r="1.5"/>
                                            <circle cx="11" cy="10" r="1.5"/>
                                            <circle cx="5" cy="17" r="1.5"/>
                                            <circle cx="11" cy="17" r="1.5"/>
                                        </svg>
                                    </button>
                                </div>
                                <div class="provider-title-section">
                                    <div class="provider-title-main">
                                        <span class="provider-display-name">{p.display_name || 'New Provider'}</span>
                                        {#if i === 0 && providers.length > 1}
                                            <span class="priority-badge">Default</span>
                                        {/if}
                                    </div>
                                    {#if p.expanded}
                                        <div class="provider-subtitle">
                                            <input type="text" bind:value={p.url} placeholder="http://localhost:8080/v1" class="url-input" oninput={() => syncProviderMeta(p)} onclick={(e) => e.stopPropagation()} />
                                        </div>
                                    {/if}
                                </div>
                                <div class="provider-actions">
                                    <span class="model-count-badge">{p.models.filter(m => m.name.trim()).length} model{p.models.filter(m => m.name.trim()).length !== 1 ? 's' : ''}</span>
                                    {#if p.default_model}
                                        {@const defaultModelInfo = parseDefaultModel(p.default_model)}
                                        <span class="default-model-badge" style="color: {getModelColor(defaultModelInfo.name)}; background: {getModelColor(defaultModelInfo.name)}22">{defaultModelInfo.name}</span>
                                    {/if}
                                    <button type="button" class="btn-remove-provider" onclick={(e) => { e.stopPropagation(); if (confirm('Remove this provider?')) removeProvider(i); }} title="Remove provider" aria-label="Remove provider">
                                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                                            <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                            <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                    </button>
                                    <button type="button" class="btn-expand" onclick={(e) => { e.stopPropagation(); toggleProvider(i); }} aria-expanded={p.expanded} aria-label={p.expanded ? 'Collapse provider details' : 'Expand provider details'} title={p.expanded ? 'Collapse' : 'Expand'}>
                                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" class:expanded={p.expanded}>
                                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                    </button>
                                </div>
                            </div>

                            <!-- Expanded details -->
                            {#if p.expanded}
                                <div class="provider-details">
                                    <div class="details-actions">
                                        <button type="button" class="btn-fetch" onclick={() => fetchModelsForProvider(i)} disabled={p.fetchingModels || !p.url.trim()} title="Fetch available models from this provider">
                                            {#if p.fetchingModels}
                                                <svg class="spin-icon" width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5" stroke-dasharray="28" stroke-dashoffset="8"/>
                                                </svg>
                                                Fetching...
                                            {:else}
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <path d="M8 2v4M8 10v4M2 8h4M10 8h4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                </svg>
                                                Fetch Models
                                            {/if}
                                        </button>
                                        <button type="button" class="btn-test" onclick={() => testProviderConnection(i)} disabled={p.testingConnection || !p.url.trim()} title="Test connection to this provider">
                                            {#if p.testingConnection}
                                                <svg class="spin-icon" width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5" stroke-dasharray="28" stroke-dashoffset="8"/>
                                                </svg>
                                                Testing...
                                            {:else}
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <path d="M2 8l4 4L14 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                                </svg>
                                                Test Connection
                                            {/if}
                                        </button>
                                        <button type="button" class="btn-default-provider" class:active={defaultProvider === p.name} onclick={() => toggleDefaultProvider(p.name)} title="Set as default provider">
                                            {defaultProvider === p.name ? '✓ Default' : 'Set default'}
                                        </button>
                                    </div>
                                    {#if p.fetchError}
                                        <div class="fetch-error">
                                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5"/>
                                                <path d="M8 5v4M8 10v1" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                            </svg>
                                            {p.fetchError}
                                        </div>
                                    {/if}
                                    {#if p.testResult}
                                        <div class="test-result" class:test-success={p.testResult.success} class:test-failure={!p.testResult.success}>
                                            {#if p.testResult.success}
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5"/>
                                                    <path d="M5 8l2 2 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                                </svg>
                                            {:else}
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5"/>
                                                    <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                </svg>
                                            {/if}
                                            {p.testResult.message}
                                        </div>
                                    {/if}
                                    <div class="provider-options">
                                        <div class="option-row">
                                            <div class="option-label-group">
                                                <label class="option-label" for={`concurrency-${i}`}>Concurrency</label>
                                                <span class="help-icon" title="Maximum simultaneous requests to this provider. Higher values allow parallel task execution but may overload the server.">
                                                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                        <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                                        <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                                                    </svg>
                                                </span>
                                            </div>
                                            <input id={`concurrency-${i}`} type="number" bind:value={p.concurrency} min="1" max="20" class="option-input" />
                                        </div>
                                    </div>
                                    <div class="models-section">
                                        <div class="models-header">
                                            <span class="models-label">Default Model</span>
                                            <button type="button" class="btn-add-model" onclick={() => addModelToProvider(i)} title="Add a model manually">
                                                <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                                                    <path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                </svg>
                                                Add Model
                                            </button>
                                        </div>
                                        <div class="models-list">
                                            {#each p.models as model, mi}
                                                <div class="model-row" class:readonly={model.readonly}>
                                                    {#if model.name}
                                                        {@const modelEffort = model.reasoning_effort || 'medium'}
                                                        {@const modelCompositeKey = modelEffort !== 'medium' ? `${model.name}|${modelEffort}` : model.name}
                                                        <button type="button" class="btn-default" class:active={p.default_model === modelCompositeKey} onclick={() => setProviderDefaultModel(i, model.name, modelEffort)} title="Set as default model" aria-label={`Set ${model.name} as default`} style="{p.default_model === modelCompositeKey ? `color: ${getModelColor(model.name)}; background: ${getModelColor(model.name)}22` : ''}">
                                                            {p.default_model === modelCompositeKey ? 'Default' : 'Set default'}
                                                        </button>
                                                    {:else}
                                                        <span class="btn-default-placeholder"></span>
                                                    {/if}
                                                    <div class="model-name-cell">
                                                        {#if model.readonly}
                                                            <span class="model-name-label">{model.name}</span>
                                                        {:else}
                                                            <input type="text" bind:value={model.name} placeholder="Model name" class="model-input" />
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
                                                    <button type="button" class="btn-remove-model" onclick={() => removeModelFromProvider(i, mi)} aria-label={`Remove ${model.name || 'model'}`} title={`Remove ${model.name || 'model'}`}>
                                                        ✕
                                                    </button>
                                                </div>
                                            {/each}
                                        </div>
                                        {#if p.models.length === 0}
                                            <div class="models-empty">No models added. Use "Fetch Models" or add manually.</div>
                                        {/if}
                                    </div>
                                    <!-- Pricing Section -->
                                    <div class="pricing-section">
                                        <div class="pricing-header">
                                            <span class="pricing-label">Pricing (USD per 1M tokens)</span>
                                            <span class="help-icon" title="Set token pricing to track costs in project stats. If Cache Write is set, it replaces Input for non-cached tokens.">
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                                    <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                                    <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                                                </svg>
                                            </span>
                                        </div>
                                        <div class="pricing-grid">
                                            <div class="pricing-field">
                                                <label for={`input-price-${i}`}>Input</label>
                                                <div class="pricing-input-wrap">
                                                    <span class="pricing-prefix">{currencySymbol}</span>
                                                    <input id={`input-price-${i}`} type="text" inputmode="decimal" value={formatPrice(p.input_price)} onfocus={(e) => { e.target.select(); }} onblur={(e) => { p.input_price = parsePrice(e.target.value); }} onkeydown={(e) => { if (e.key === 'Enter') e.target.blur(); }} placeholder="0.00" class="pricing-input" />
                                                </div>
                                            </div>
                                            <div class="pricing-field">
                                                <label for={`output-price-${i}`}>Output</label>
                                                <div class="pricing-input-wrap">
                                                    <span class="pricing-prefix">{currencySymbol}</span>
                                                    <input id={`output-price-${i}`} type="text" inputmode="decimal" value={formatPrice(p.output_price)} onfocus={(e) => { e.target.select(); }} onblur={(e) => { p.output_price = parsePrice(e.target.value); }} onkeydown={(e) => { if (e.key === 'Enter') e.target.blur(); }} placeholder="0.00" class="pricing-input" />
                                                </div>
                                            </div>
                                            <div class="pricing-field">
                                                <label for={`cache-read-price-${i}`}>Cache Read</label>
                                                <div class="pricing-input-wrap">
                                                    <span class="pricing-prefix">{currencySymbol}</span>
                                                    <input id={`cache-read-price-${i}`} type="text" inputmode="decimal" value={formatPrice(p.cache_read_price)} onfocus={(e) => { e.target.select(); }} onblur={(e) => { p.cache_read_price = parsePrice(e.target.value); }} onkeydown={(e) => { if (e.key === 'Enter') e.target.blur(); }} placeholder="0.00" class="pricing-input" />
                                                </div>
                                            </div>
                                            <div class="pricing-field">
                                                <label for={`cache-write-price-${i}`}>Cache Write</label>
                                                <div class="pricing-input-wrap">
                                                    <span class="pricing-prefix">{currencySymbol}</span>
                                                    <input id={`cache-write-price-${i}`} type="text" inputmode="decimal" value={formatPrice(p.cache_write_price)} onfocus={(e) => { e.target.select(); }} onblur={(e) => { p.cache_write_price = parsePrice(e.target.value); }} onkeydown={(e) => { if (e.key === 'Enter') e.target.blur(); }} placeholder="0.00" class="pricing-input" />
                                                </div>
                                            </div>
                                        </div>
                                    </div>
                                </div>
                            {/if}
                        </div>
                    {/each}
                </div>
                {#if providers.length === 0}
                    <div class="providers-empty">
                        <svg width="40" height="40" viewBox="0 0 40 40" fill="none" class="empty-icon">
                            <rect x="4" y="8" width="32" height="24" rx="3" stroke="currentColor" stroke-width="1.5"/>
                            <path d="M4 16h32" stroke="currentColor" stroke-width="1.5"/>
                            <circle cx="10" cy="12" r="1" fill="currentColor"/>
                            <circle cx="15" cy="12" r="1" fill="currentColor"/>
                            <circle cx="20" cy="12" r="1" fill="currentColor"/>
                        </svg>
                        <p>No providers configured</p>
                        <span>Add an LLM provider to get started. Supports any OpenAI-compatible API.</span>
                    </div>
                {/if}
            </fieldset>

            <!-- General Section -->
            <fieldset class="section">
                <h3 class="section-label">General</h3>
                <label class="field-label">
                    Workspace Directory
                    <input type="text" bind:value={workspaceDir} placeholder="./workspace" />
                    <span class="field-help">Directory where task worktrees are created</span>
                </label>
                <div class="field-label">
                    <span class="field-label-inline">Database</span>
                    <div class="readonly-value">{config?.database_path || '—'}</div>
                    <span class="field-help">Path to the server's SQLite database</span>
                </div>
            </fieldset>

            <!-- Task Defaults Section -->
            <fieldset class="section">
                <h3 class="section-label">Task Defaults</h3>
                <p class="section-description">These settings apply to newly created tasks</p>

                <div class="toggle-field">
                    <div class="toggle-label-group">
                        <span id="toggle-autocommit" class="toggle-label">Auto-commit</span>
                        <span class="help-icon" title="Automatically commit changes when the task completes">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                            </svg>
                        </span>
                    </div>
                    <button
                        type="button"
                        role="switch"
                        class="toggle-switch"
                        class:active={defaultAutoCommit}
                        aria-checked={defaultAutoCommit}
                        aria-labelledby="toggle-autocommit"
                        onclick={() => defaultAutoCommit = !defaultAutoCommit}
                        title="Automatically commit changes when the task completes"
                    >
                        <span class="toggle-knob"></span>
                    </button>
                </div>

                <div class="toggle-field">
                    <div class="toggle-label-group">
                        <span id="toggle-planmode" class="toggle-label">Plan mode</span>
                        <span class="help-icon" title="Task describes its plan before making changes">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                            </svg>
                        </span>
                    </div>
                    <button
                        type="button"
                        role="switch"
                        class="toggle-switch"
                        class:active={defaultPlanMode}
                        aria-checked={defaultPlanMode}
                        aria-labelledby="toggle-planmode"
                        onclick={() => defaultPlanMode = !defaultPlanMode}
                        title="Task describes its plan before making changes"
                    >
                        <span class="toggle-knob"></span>
                    </button>
                </div>

                <div class="field-row">
                    <div class="option-label-group">
                        <label class="field-label-inline" for="default-max-failed">Max failed turns</label>
                        <span class="help-icon" title="Stop task after this many consecutive LLM failures">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                            </svg>
                        </span>
                    </div>
                    <input id="default-max-failed" type="number" bind:value={defaultMaxFailedTurns} min="1" max="20" class="option-input" />
                </div>
            </fieldset>

            {#if error}
                <div id="config-error" class="error" role="alert" aria-live="assertive">{error}</div>
            {/if}

            <div class="modal-footer">
                <button type="button" onclick={handleClose} title="Cancel and discard changes">Cancel</button>
                <button type="submit" class="btn-primary" disabled={loading} title="Save settings">
                    {loading ? 'Saving...' : 'Save'}
                </button>
            </div>
        </form>
    </div>
</div>

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0,0,0,0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 100;
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 640px;
        max-width: 90vw;
        max-height: 90vh;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
    }
    .modal-header h2 {
        font-size: 1rem;
        font-weight: 600;
    }
    .close-btn {
        font-size: 1.25rem;
        color: var(--color-text-primary);
        padding: 4px 8px;
        line-height: 1;
    }
    .modal-body {
        padding: 20px 20px 0;
        display: flex;
        flex-direction: column;
        gap: 24px;
        overflow-y: auto;
        flex: 1;
        min-height: 0;
        scroll-behavior: smooth;
    }
    /* Scroll depth indicator */
    .modal-body::-webkit-scrollbar {
        width: 6px;
    }
    .modal-body::-webkit-scrollbar-track {
        background: transparent;
    }
    .modal-body::-webkit-scrollbar-thumb {
        background: var(--color-surface-4);
        border-radius: 3px;
    }

    /* Screen-reader only utility */
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border: 0;
    }

    /* Section fieldsets */
    .section {
        border: none;
        padding: 0;
        margin: 0;
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    /* Section headings */
    .section-label {
        margin: 0;
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: #B0BAC4;
    }

    /* Section header row (label + action button) */
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }

    /* Field labels */
    .field-label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .field-label input {
        width: 100%;
    }
    .field-help {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .readonly-value {
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: 0.8125rem;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-secondary);
        user-select: all;
    }

    /* Section description */
    .section-description {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        margin: -4px 0 4px;
    }

    /* Toggle field */
    .toggle-field {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
    }
    .toggle-field + .toggle-field {
        border-top: 1px solid var(--color-border);
    }
    .toggle-label-group {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .toggle-label {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .toggle-switch {
        width: 40px;
        height: 22px;
        background: var(--color-surface-4);
        border-radius: 11px;
        position: relative;
        transition: background 0.2s ease;
        flex-shrink: 0;
    }
    .toggle-switch.active {
        background: var(--color-accent);
    }
    .toggle-knob {
        width: 16px;
        height: 16px;
        background: white;
        border-radius: 50%;
        position: absolute;
        top: 3px;
        left: 3px;
        transition: transform 0.2s ease;
    }
    .toggle-switch.active .toggle-knob {
        transform: translateX(18px);
    }

    /* Field row (inline label + input) */
    .field-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
        border-top: 1px solid var(--color-border);
        gap: 12px;
    }
    .field-label-inline {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }

    /* Radio group */
    .radio-group {
        padding: 8px 0;
        border-top: 1px solid var(--color-border);
    }
    .radio-group-label {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
        display: block;
        margin-bottom: 8px;
    }
    .radio-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .radio-option {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 8px 10px;
        border-radius: var(--radius-md);
        cursor: pointer;
        transition: background 0.1s ease;
    }
    .radio-option:hover {
        background: var(--color-surface-2);
    }
    .radio-option input[type="radio"] {
        margin-top: 3px;
        flex-shrink: 0;
        accent-color: var(--color-accent);
    }
    .radio-text {
        display: flex;
        flex-direction: column;
        gap: 1px;
        min-width: 0;
    }
    .radio-title {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .radio-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        line-height: 1.3;
    }

    /* Inputs */
    input[type="text"], input[type="number"], select {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }
    input:focus-visible, select:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    select {
        cursor: pointer;
    }

    /* Buttons */
    .btn-small {
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
    }
    .btn-small:hover {
        background: var(--color-surface-4);
    }

    /* Providers list */
    .providers-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    /* Providers empty state */
    .providers-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 8px;
        padding: 32px 16px;
        text-align: center;
        color: var(--color-text-secondary);
    }
    .providers-empty p {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .providers-empty span {
        font-size: 0.875rem;
        max-width: 280px;
    }
    .empty-icon {
        color: var(--color-text-tertiary);
        margin-bottom: 4px;
    }

    /* Provider card */
    .provider-card {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        overflow: hidden;
        transition: border-color 0.15s ease;
    }
    .provider-card:hover {
        border-color: var(--color-border-bright);
    }

    /* Card header (always visible, clickable to expand/collapse) */
    .provider-card-header {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 12px 14px;
        cursor: pointer;
        user-select: none;
    }
    .provider-card-header:hover {
        background: var(--color-surface-3);
    }
    .provider-card-header input {
        cursor: text;
    }

    /* Provider title section */
    .provider-title-section {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .provider-title-main {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .provider-display-name {
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .priority-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-status-green);
        background: rgba(63, 185, 80, 0.1);
        padding: 2px 6px;
        border-radius: 3px;
    }
    .provider-subtitle {
        display: flex;
        align-items: center;
    }

    /* URL input in header */
    .url-input {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        background: transparent;
        border: 1px solid transparent;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        width: 100%;
        min-width: 0;
        font-family: var(--font-mono, monospace);
    }
    .url-input:focus {
        background: var(--color-surface-0);
        border-color: var(--color-accent);
        color: var(--color-text-primary);
        outline: none;
    }
    .url-input::placeholder {
        color: var(--color-text-tertiary);
    }

    /* Provider actions (right side of header) */
    .provider-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }
    .model-count-badge {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        background: var(--color-surface-3);
        padding: 2px 8px;
        border-radius: 10px;
        font-weight: 500;
        white-space: nowrap;
    }
    .default-model-badge {
        font-size: 0.75rem;
        padding: 2px 8px;
        border-radius: 10px;
        font-weight: 500;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        max-width: 160px;
        font-family: var(--font-mono, monospace);
    }

    /* Expand/collapse button */
    .btn-expand {
        padding: 4px;
        color: var(--color-text-secondary);
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 4px;
        transition: transform 0.15s ease, color 0.15s ease;
    }
    .btn-expand:hover {
        color: var(--color-text-primary);
        background: var(--color-surface-4);
    }
    .btn-expand svg {
        transition: transform 0.2s ease;
    }
    .btn-expand svg.expanded {
        transform: rotate(180deg);
    }

    /* Reorder buttons */
    .reorder-buttons {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 1px;
        flex-shrink: 0;
        padding-top: 2px;
    }
    /* Drag handle button */
    .btn-drag-handle {
        background: none;
        border: none;
        padding: 3px;
        cursor: grab;
        color: var(--color-text-primary);
        opacity: 0.4;
        display: flex;
        align-items: center;
        justify-content: center;
        line-height: 1;
        border-radius: 3px;
        touch-action: none;
    }
    .btn-drag-handle:hover {
        color: var(--color-accent);
        opacity: 1;
        background: var(--color-surface-3);
    }
    .btn-drag-handle:active {
        cursor: grabbing;
    }

    /* Provider card dragging state */
    .provider-card.dragging {
        opacity: 0.4;
        border-color: var(--color-accent);
    }

    /* Drag preview (follows cursor) */
    .drag-preview {
        position: fixed;
        pointer-events: none;
        z-index: 10001;
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-accent);
        opacity: 0.8;
    }

    /* Remove provider button */
    .btn-remove-provider {
        color: var(--color-text-tertiary);
        padding: 5px;
        line-height: 1;
        flex-shrink: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: var(--radius-sm);
        transition: color 0.15s ease, background 0.15s ease;
    }
    .btn-remove-provider:hover {
        color: var(--color-status-red);
        background: rgba(248, 81, 73, 0.1);
    }

    /* Expanded details panel */
    .provider-details {
        padding: 0 14px 14px;
        display: flex;
        flex-direction: column;
        gap: 12px;
        border-top: 1px solid var(--color-border);
        padding-top: 12px;
    }

    /* Action buttons row (Fetch Models, Test Connection) */
    .details-actions {
        display: flex;
        gap: 8px;
    }
    .btn-fetch,
    .btn-test {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 600;
        cursor: pointer;
        white-space: nowrap;
        transition: background 0.15s ease, opacity 0.15s ease;
    }
    .btn-fetch {
        background: var(--color-accent);
        color: white;
    }
    .btn-fetch:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-fetch:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .btn-test {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .btn-test:hover:not(:disabled) {
        background: var(--color-surface-4);
    }
    .btn-test:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .btn-default-provider {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 600;
        cursor: pointer;
        white-space: nowrap;
        transition: background 0.15s ease, color 0.15s ease;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border: none;
    }
    .btn-default-provider:hover {
        background: var(--color-surface-4);
    }
    .btn-default-provider.active {
        color: var(--color-status-green);
        background: rgba(63, 185, 80, 0.1);
    }

    /* Spin animation for loading icons */
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
    .spin-icon {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }

    /* Fetch error */
    .fetch-error {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--color-status-red);
        font-weight: 500;
        background: rgba(248, 81, 73, 0.08);
        padding: 6px 10px;
        border-radius: var(--radius-sm);
    }

    /* Test connection result */
    .test-result {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        font-weight: 500;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
    }
    .test-result.test-success {
        color: var(--color-status-green);
        background: rgba(63, 185, 80, 0.08);
    }
    .test-result.test-failure {
        color: var(--color-status-red);
        background: rgba(248, 81, 73, 0.08);
    }

    /* Provider options (concurrency, etc.) */
    .provider-options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .option-row {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .option-label-group {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .option-label {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }
    .option-input {
        width: 64px;
        min-width: 64px;
        flex-shrink: 0;
        padding: 4px 8px;
    }
    .option-input::-webkit-inner-spin-button,
    .option-input::-webkit-outer-spin-button {
        -webkit-appearance: none;
    }
    .option-input[type="number"] {
        -moz-appearance: textfield;
    }

    /* Help icon tooltip */
    .help-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-tertiary);
        cursor: help;
        position: relative;
    }
    .help-icon:hover {
        color: var(--color-text-secondary);
    }

    /* Pricing section */
    .pricing-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }
    .pricing-header {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .pricing-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .pricing-grid {
        display: grid;
        grid-template-columns: repeat(4, 1fr);
        gap: 6px;
    }
    .pricing-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .pricing-field label {
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .pricing-input-wrap {
        display: flex;
        align-items: center;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
        transition: border-color 0.15s ease;
        width: fit-content;
        max-width: 100%;
    }
    .pricing-input-wrap:focus-within {
        border-color: var(--color-accent);
    }
    .pricing-prefix {
        padding: 0 2px 0 6px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .pricing-input {
        width: 68px;
        min-width: 0;
        border: none;
        background: transparent;
        padding: 4px 6px;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
    }
    .pricing-input:focus,
    .pricing-input:focus-visible {
        outline: none;
        border: none;
    }
    .pricing-input::placeholder {
        color: var(--color-text-tertiary);
    }
    /* Remove number input spinners */
    .pricing-input::-webkit-outer-spin-button,
    .pricing-input::-webkit-inner-spin-button {
        -webkit-appearance: none;
        margin: 0;
    }
    .pricing-input[type=number] {
        -moz-appearance: textfield;
    }

    /* Models section */
    .models-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .models-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .models-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .btn-add-model {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
    }
    .btn-add-model:hover {
        background: var(--color-surface-4);
    }

    /* Models list */
    .models-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .models-empty {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        padding: 8px 0;
        font-style: italic;
    }

    /* Model row */
    .model-row {
        display: grid;
        grid-template-columns: 90px 1fr 120px 28px;
        align-items: center;
        gap: 8px;
    }
    .model-row.readonly {
        padding: 5px 8px;
        background: var(--color-surface-0);
        border-radius: var(--radius-sm);
    }
    .model-name-cell {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
    }
    .in-use-badge {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.03em;
        color: var(--color-status-blue);
        background: rgba(76, 154, 255, 0.12);
        padding: 1px 5px;
        border-radius: 3px;
        flex-shrink: 0;
    }
    .context-window-badge {
        font-size: 0.65rem;
        font-weight: 500;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 1px 5px;
        border-radius: 3px;
        flex-shrink: 0;
        white-space: nowrap;
    }

    /* Model name display */
    .model-name-label {
        font-size: 0.875rem;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    /* Model name input */
    .model-input {
        padding: 5px 8px;
        font-size: 0.875rem;
        width: 100%;
    }

    /* Reasoning effort select */
    .reasoning-effort-select {
        background: var(--color-surface-0);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-sm);
        padding: 4px 6px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        width: 100%;
        min-width: 0;
        font-family: inherit;
        transition: border-color 0.15s ease, color 0.15s ease;
    }
    .reasoning-effort-select:hover {
        border-color: var(--color-border-bright);
        color: var(--color-text-primary);
    }
    .reasoning-effort-select:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    .reasoning-effort-select optgroup {
        font-style: normal;
    }

    /* Default model button */
    .btn-default {
        padding: 3px 10px;
        border-radius: 10px;
        font-size: 0.75rem;
        font-weight: 600;
        cursor: pointer;
        text-align: center;
        white-space: nowrap;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        transition: all 0.15s ease;
        justify-self: start;
    }
    .btn-default:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .btn-default.active {
        background: rgba(227, 179, 65, 0.18);
        color: var(--color-status-yellow);
    }
    .btn-default-placeholder {
        width: 90px;
    }

    /* Remove model button */
    .btn-remove-model {
        color: var(--color-text-secondary);
        padding: 0;
        line-height: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: var(--radius-sm);
        font-size: 1.125rem;
        width: 28px;
        height: 28px;
        transition: color 0.15s ease, background 0.15s ease;
    }
    .btn-remove-model:hover {
        color: var(--color-status-red);
        background: rgba(248, 81, 73, 0.1);
    }

    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
        font-weight: 500;
    }
    .modal-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding: 12px 20px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
        position: sticky;
        bottom: 0;
        z-index: 10;
    }
    .btn-primary {
        padding: 8px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent);
        color: white;
        font-weight: 500;
        cursor: pointer;
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    button[type="button"] {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        cursor: pointer;
        border: none;
        font-family: inherit;
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
        .provider-card-header {
            padding: 14px 12px;
        }
        .provider-details {
            padding: 0 12px 14px;
            padding-top: 12px;
        }
        .details-actions {
            flex-direction: column;
        }
        .btn-fetch,
        .btn-test {
            justify-content: center;
        }
        .model-row {
            grid-template-columns: 1fr;
            gap: 4px;
        }
        .btn-default,
        .btn-default-placeholder {
            width: 100%;
            justify-self: stretch;
        }
        button[type="button"],
        .btn-primary,
        .btn-small,
        .btn-fetch,
        .btn-test,
        .btn-add-model {
            min-height: 44px;
        }
        /* Exclude toggle-switch from min-height/padding — it has its own designed size */
        button.toggle-switch {
            min-height: 0 !important;
            padding: 0;
            width: 40px;
            height: 22px;
        }
        /* Make the entire toggle row a proper touch target */
        .toggle-field {
            min-height: 44px;
        }

        /* Provider card header — compact on mobile */
        .btn-drag-handle {
            display: flex;
            min-height: 0 !important;
            padding: 8px 3px;
        }
        .priority-badge {
            display: none;
        }
        .model-count-badge {
            display: none;
        }
        .default-model-badge {
            display: none;
        }
        /* Icon-only buttons should not be stretched to 44px */
        button.btn-remove-provider,
        button.btn-expand {
            min-height: 0 !important;
            padding: 5px;
        }
        .provider-actions {
            flex-wrap: wrap;
        }
    }

    .spin-icon {
        animation: spin 0.8s linear infinite;
        display: inline-block;
    }
    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
</style>
