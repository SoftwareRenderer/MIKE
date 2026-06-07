<script lang="ts">
    import type { Provider } from '$lib/types';

    interface Props {
        providers: Provider[];
        currentModel: string;
        currentUrl: string;
        disabled?: boolean;
        onSelect: (providerName: string, modelName: string, providerUrl: string) => void;
    }

    let { providers, currentModel, currentUrl, disabled = false, onSelect }: Props = $props();

    let selectedProviderUrl = $state(currentUrl || '');
    let providerDropdownOpen = $state(false);
    let modelDropdownOpen = $state(false);

    let providerTriggerEl: HTMLButtonElement | null = null;
    let modelTriggerEl: HTMLButtonElement | null = null;

    let providerRect = $state<{ top: number; left: number } | null>(null);
    let modelRect = $state<{ top: number; left: number } | null>(null);

    let selectedProvider = $derived(
        providers.find(p => p.url === selectedProviderUrl) ||
        providers.find(p => p.url === currentUrl) ||
        providers[0] ||
        null
    );

    let availableModels = $derived(
        selectedProvider?.models.filter(m => m.trim()) || []
    );

    let activeProviders = $derived(
        providers.filter(p => p.models.length > 0)
    );

    $effect(() => {
        selectedProviderUrl = currentUrl || '';
    });

    $effect(() => {
        if (currentModel && providers.length > 0) {
            const providerWithModel = providers.find(p => p.models.includes(currentModel));
            if (providerWithModel && providerWithModel.url !== selectedProviderUrl) {
                selectedProviderUrl = providerWithModel.url;
            }
        }
    });

    function getRect(el: HTMLElement | null): { top: number; left: number } | null {
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { top: r.bottom + 6, left: r.left };
    }

    function toggleProviderDropdown() {
        if (disabled) return;
        if (!providerDropdownOpen) {
            providerRect = getRect(providerTriggerEl);
            modelDropdownOpen = false;
        }
        providerDropdownOpen = !providerDropdownOpen;
    }

    function toggleModelDropdown() {
        if (disabled) return;
        if (!modelDropdownOpen) {
            modelRect = getRect(modelTriggerEl);
            providerDropdownOpen = false;
        }
        modelDropdownOpen = !modelDropdownOpen;
    }

    function selectProvider(provider: Provider) {
        selectedProviderUrl = provider.url;
        providerDropdownOpen = false;
        const modelToSelect = provider.default_model && provider.models.includes(provider.default_model)
            ? provider.default_model
            : provider.models[0];
        if (modelToSelect) {
            onSelect(provider.name, modelToSelect, provider.url);
        }
    }

    function selectModel(model: string) {
        if (!selectedProvider) return;
        onSelect(selectedProvider.name, model, selectedProvider.url);
        modelDropdownOpen = false;
    }

    function handleClickOutside(e: MouseEvent) {
        const target = e.target as Node;
        if (providerDropdownOpen && providerTriggerEl && !providerTriggerEl.contains(target)) {
            // Check if click is inside the portal dropdown
            const portal = document.querySelector('.portal-provider-dropdown');
            if (portal && portal.contains(target)) return;
            providerDropdownOpen = false;
        }
        if (modelDropdownOpen && modelTriggerEl && !modelTriggerEl.contains(target)) {
            const portal = document.querySelector('.portal-model-dropdown');
            if (portal && portal.contains(target)) return;
            modelDropdownOpen = false;
        }
    }

    $effect(() => {
        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    });
</script>

<div class="model-selector">
    {#if activeProviders.length > 1}
        <button
            type="button"
            class="selector-trigger provider-trigger"
            class:open={providerDropdownOpen}
            class:disabled={disabled}
            bind:this={providerTriggerEl}
            onclick={toggleProviderDropdown}
            title="Select provider"
        >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                <rect x="2" y="2" width="12" height="12" rx="2" stroke="currentColor" stroke-width="1.5"/>
                <path d="M6 8h4M8 6v4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
            <span class="selector-label">
                {selectedProvider?.display_name || selectedProvider?.name || 'Provider'}
            </span>
            <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none">
                <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    {/if}

    <button
        type="button"
        class="selector-trigger model-trigger"
        class:open={modelDropdownOpen}
        class:disabled={disabled}
        bind:this={modelTriggerEl}
        onclick={toggleModelDropdown}
        title="Select model"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
            <path d="M2 4l6 5 6-5M2 4v8l6 5 6-5V4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span class="selector-label model-name" class:current={!!currentModel}>
            {currentModel || 'Select model'}
        </span>
        <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none">
            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>
</div>

<!-- Provider dropdown (rendered in shadow DOM to escape overflow clipping) -->
{#if providerDropdownOpen && providerRect}
    <svelte:element this="div" class="portal-provider-dropdown" style="position:fixed;top:{providerRect.top}px;left:{providerRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#each activeProviders as provider}
            <button type="button" class="provider-option" class:active={provider.url === selectedProviderUrl} onclick={() => selectProvider(provider)}>
                <span class="provider-name">{provider.display_name || provider.name}</span>
                <span class="model-count">{provider.models.length} model{provider.models.length !== 1 ? 's' : ''}</span>
                {#if provider.url === selectedProviderUrl}
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                        <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                {/if}
            </button>
        {/each}
    </svelte:element>
{/if}

<!-- Model dropdown -->
{#if modelDropdownOpen && modelRect}
    <svelte:element this="div" class="portal-model-dropdown" style="position:fixed;top:{modelRect.top}px;left:{modelRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#if availableModels.length === 0}
            <div class="empty">No models available</div>
        {:else}
            {#each availableModels as model}
                <button type="button" class="model-option" class:active={model === currentModel} onclick={() => selectModel(model)}>
                    <span class="option-name">{model}</span>
                    {#if model === currentModel}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {/if}
                </button>
            {/each}
        {/if}
    </svelte:element>
{/if}

<style>
    .model-selector {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .selector-trigger {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: 12px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: 11px;
        font-family: inherit;
        cursor: pointer;
        transition: all 0.15s ease;
        white-space: nowrap;
    }
    .selector-trigger:hover:not(.disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border-color: var(--color-accent);
    }
    .selector-trigger.open {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
        color: var(--color-text-primary);
    }
    .selector-trigger.disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .selector-label {
        max-width: 140px;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .selector-label.current {
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .provider-trigger .selector-label {
        max-width: 100px;
    }

    .chevron {
        transition: transform 0.15s ease;
        flex-shrink: 0;
    }
    .selector-trigger.open .chevron {
        transform: rotate(180deg);
    }

    .empty {
        padding: 12px 16px;
        font-size: 12px;
        color: var(--color-text-tertiary);
        text-align: center;
    }

    .provider-option {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        border: none;
        background: none;
        color: var(--color-text-primary);
        font-size: 12px;
        font-family: inherit;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s ease;
    }
    .provider-option:hover {
        background: var(--color-surface-3);
    }
    .provider-option.active {
        background: color-mix(in srgb, var(--color-accent) 10%, transparent);
        color: var(--color-accent);
    }
    .provider-name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-weight: 500;
    }
    .model-count {
        font-size: 10px;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }

    .model-option {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        border: none;
        background: none;
        color: var(--color-text-primary);
        font-size: 12px;
        font-family: inherit;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s ease;
    }
    .model-option:hover {
        background: var(--color-surface-3);
    }
    .model-option.active {
        background: color-mix(in srgb, var(--color-accent) 10%, transparent);
        color: var(--color-accent);
    }
    .option-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
</style>
