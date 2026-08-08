<script lang="ts">
    import { slide } from 'svelte/transition';
    import { dialogStore } from '$lib/stores';
    import { getProviderDisplayName } from '$lib/utils/providers';
    import AgentSection from './AgentSection.svelte';
    import ModelList from './ModelList.svelte';
    import PricingSection from './PricingSection.svelte';

    interface ModelEntry {
        name: string;
        readonly: boolean;
        reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
        context_window?: number | null;
        input_price?: number | null;
        output_price?: number | null;
        cache_read_price?: number | null;
        cache_write_price?: number | null;
        power_consumption_w?: number | null;
    }

    interface ProviderDraft {
        name: string;
        display_name?: string; // only agent providers carry a manifest-derived display name
        kind: 'llm' | 'agent';
        url: string;
        api_key: string;
        agent_config_dir?: string;
        models: ModelEntry[];
        concurrency: number;
        input_price: number;
        output_price: number;
        cache_read_price: number;
        cache_write_price: number;
        power_consumption_w: number;
        electricity_cost_per_kwh: number;
        fetchingModels: boolean;
        fetchError: string;
        expanded: boolean;
        testingConnection: boolean;
        testResult: { success: boolean; message: string } | null;
    }

    let { providers, provider, index, encryptionEnabled, currencySymbol, getModelColor, onDragStart, onRemove, onFetchModels, onTestConnection, onAddModel, onRemoveModel, isDragging, onDirty }: {
        providers: ProviderDraft[];
        provider: ProviderDraft;
        index: number;
        encryptionEnabled: boolean;
        currencySymbol: string;
        getModelColor: (m: string) => string;
        onDragStart: (e: PointerEvent) => void;
        onRemove: () => void;
        onFetchModels: () => void;
        onTestConnection: () => void;
        onAddModel: () => void;
        onRemoveModel: (mi: number) => void;
        isDragging: boolean;
        onDirty: () => void;
    } = $props();

    function handleToggle() {
        provider.expanded = !provider.expanded;
    }

    async function handleRemove(e: MouseEvent) {
        e.stopPropagation();
        const confirmed = await dialogStore.showConfirm({
            title: 'Remove Provider',
            message: 'Remove this provider? This cannot be undone.',
            confirmText: 'Remove',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        onRemove();
    }
</script>

<div class="provider-card" data-index={index} class:dragging={isDragging}>
    <!-- Card header: always visible -->
    <div class="provider-card-header" onclick={handleToggle} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleToggle(); } }} role="button" aria-expanded={provider.expanded} aria-label={`Toggle ${provider.name || provider.url ? getProviderDisplayName(provider, providers) : 'New Provider'} settings`} tabindex="0">
        <div class="reorder-buttons">
            <button type="button" class="btn-drag-handle" onpointerdown={onDragStart} title="Drag to reorder" aria-label="Drag to reorder provider">
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
                {#if provider.kind === 'agent'}
                    <span class="agent-icon" title="Agent">
                        <svg width="18" height="18" viewBox="0 0 16 16" fill="none">
                            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.2"/>
                            <circle cx="6" cy="9" r="1" fill="currentColor"/>
                            <circle cx="10" cy="9" r="1" fill="currentColor"/>
                            <path d="M8 2v3M5 4h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                        </svg>
                    </span>
                {/if}
                <span class="provider-display-name">{provider.name || provider.url ? getProviderDisplayName(provider, providers) : 'New Provider'}</span>
            </div>
        </div>
        <div class="provider-actions">
            <button type="button" class="btn-remove-provider" onclick={handleRemove} title="Remove provider" aria-label="Remove provider">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                    <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
            <button type="button" class="btn-expand" onclick={(e) => { e.stopPropagation(); handleToggle(); }} aria-expanded={provider.expanded} aria-label={provider.expanded ? 'Collapse provider details' : 'Expand provider details'} title={provider.expanded ? 'Collapse' : 'Expand'}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" class:expanded={provider.expanded}>
                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
        </div>
    </div>

    <!-- Expanded details -->
    {#if provider.expanded}
        <div class="provider-details" transition:slide={{ duration: 200 }}>
            {#if provider.kind === 'agent'}
                <AgentSection agentName={provider.name} />
            {:else}
                <!-- LLM Provider Fields -->
                <div class="url-and-key-row">
                    <div class="url-field">
                        <label class="url-label" for={`provider-url-${index}`}>Provider URL</label>
                        <input id={`provider-url-${index}`} type="text" bind:value={provider.url} placeholder="http://localhost:8080/v1" oninput={() => onDirty?.()} onclick={(e) => e.stopPropagation()} aria-label="Provider URL" />
                    </div>
                    <div class="api-key-field">
                        <label class="api-key-label" for={`provider-key-${index}`}>API key <span class="optional-text">(optional)</span></label>
                        <input
                            id={`provider-key-${index}`}
                            type="password"
                            bind:value={provider.api_key}
                            placeholder="API key..."
                            onclick={(e) => e.stopPropagation()}
                            onchange={() => onDirty?.()}
                        />
                    </div>
                </div>

                <div class="details-actions">
                    <button type="button" class="btn-fetch" onclick={onFetchModels} disabled={provider.fetchingModels || !provider.url.trim()} title="Fetch available models from this provider">
                        {#if provider.fetchingModels}
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
                    <button type="button" class="btn-test" onclick={onTestConnection} disabled={provider.testingConnection || !provider.url.trim()} title="Test connection to this provider">
                        {#if provider.testingConnection}
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
                </div>
                {#if provider.fetchError}
                    <div class="fetch-error">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                            <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.5"/>
                            <path d="M8 5v4M8 10v1" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        </svg>
                        {provider.fetchError}
                    </div>
                {/if}
                {#if provider.testResult}
                    <div class="test-result" class:test-success={provider.testResult.success} class:test-failure={!provider.testResult.success}>
                        {#if provider.testResult.success}
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
                        {provider.testResult.message}
                    </div>
                {/if}
                <div class="provider-options">
                    <div class="option-row">
                        <div class="option-label-group">
                            <label class="option-label" for={`concurrency-${index}`}>Concurrency</label>
                            <span class="help-icon" title="Maximum simultaneous requests to this provider.">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                    <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                                    <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                                </svg>
                            </span>
                        </div>
                        <input id={`concurrency-${index}`} type="number" bind:value={provider.concurrency} min="1" max="20" class="option-input" onchange={() => onDirty?.()} />
                    </div>
                </div>

                <ModelList
                    models={provider.models}
                    onAdd={onAddModel}
                    onRemove={onRemoveModel}
                    {currencySymbol}
                    {index}
                    onDirty={onDirty}
                />

                <PricingSection {provider} {currencySymbol} {index} onDirty={onDirty} />
            {/if}
        </div>
    {/if}
</div>

<style>
    .provider-card {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        overflow: hidden;
        transition: border-color 0.15s, opacity 0.15s;
    }
    .provider-card.dragging {
        opacity: 0.4;
    }
    .provider-card-header {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 10px 12px;
        cursor: pointer;
        user-select: none;
    }
    .provider-card-header:hover {
        background: var(--color-surface-1);
    }
    .reorder-buttons {
        display: flex;
        align-items: center;
        gap: 2px;
        flex-shrink: 0;
    }
    .btn-drag-handle {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 24px;
        color: var(--color-text-secondary);
        opacity: 0.4;
        background: none;
        border: none;
        cursor: grab;
        padding: 0;
    }
    .btn-drag-handle:hover {
        opacity: 0.8;
    }
    .provider-title-section {
        flex: 1;
        min-width: 0;
    }
    .provider-title-main {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .agent-icon {
        display: flex;
        align-items: center;
        color: var(--color-status-purple);
        flex-shrink: 0;
    }
    .provider-display-name {
        font-size: 0.875rem;
        font-weight: 600;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .provider-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }
    .btn-remove-provider {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        cursor: pointer;
    }
    .btn-remove-provider:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    .btn-expand {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        border-radius: var(--radius-sm);
        cursor: pointer;
    }
    .btn-expand:hover {
        background: var(--color-surface-1);
    }
    .btn-expand svg.expanded {
        transform: rotate(180deg);
    }
    .provider-details {
        padding: 0 12px 12px;
        border-top: 1px solid var(--color-border);
    }
    .url-and-key-row {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 8px;
        margin-top: 12px;
    }
    .url-field,
    .api-key-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .url-label,
    .api-key-label {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .optional-text {
        font-size: 0.6875rem;
        opacity: 0.6;
    }
    .url-field input,
    .api-key-field input {
        font-size: 0.8125rem;
        padding: 6px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
    }
    .url-field input:focus,
    .api-key-field input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .details-actions {
        display: flex;
        gap: 6px;
        margin-top: 10px;
        flex-wrap: wrap;
    }
    .btn-fetch,
    .btn-test {
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        cursor: pointer;
    }
    .btn-fetch:hover:not(:disabled),
    .btn-test:hover:not(:disabled) {
        border-color: var(--color-text-secondary);
    }
    .btn-fetch:disabled,
    .btn-test:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .spin-icon {
        animation: spin 1s linear infinite;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
    .fetch-error {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-top: 8px;
        padding: 6px 8px;
        font-size: 0.75rem;
        color: var(--color-danger);
        background: rgba(248, 81, 73, 0.1);
        border-radius: var(--radius-sm);
    }
    .test-result {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-top: 8px;
        padding: 6px 8px;
        font-size: 0.75rem;
        border-radius: var(--radius-sm);
    }
    .test-result.test-success {
        color: var(--color-success);
        background: rgba(63, 185, 80, 0.1);
    }
    .test-result.test-failure {
        color: var(--color-danger);
        background: rgba(248, 81, 73, 0.1);
    }
    .provider-options {
        margin-top: 10px;
        padding: 8px 10px;
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
    }
    .option-row {
        display: flex;
        align-items: center;
        gap: 10px;
    }
    .option-label-group {
        display: flex;
        align-items: center;
        gap: 4px;
        flex: 1;
    }
    .option-label {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .help-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-secondary);
        opacity: 0.5;
        cursor: help;
    }
    .help-icon:hover {
        opacity: 0.8;
    }
    .option-input {
        width: 60px;
        font-size: 0.8125rem;
        padding: 3px 6px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        text-align: center;
    }
    .option-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
</style>
