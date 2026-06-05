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

    let isOpen = $state(false);
    let dropdownEl: HTMLDivElement | null = null;

    // Find which provider the current model belongs to
    let currentProvider = $derived(
        providers.find(p => p.url === currentUrl) || null
    );

    // All models grouped by provider
    let allModels = $derived(
        providers.filter(p => p.models.length > 0)
    );

    function toggle() {
        if (disabled) return;
        isOpen = !isOpen;
    }

    function selectModel(provider: Provider, model: string) {
        onSelect(provider.name, model, provider.url);
        isOpen = false;
    }

    function handleClickOutside(e: MouseEvent) {
        if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
            isOpen = false;
        }
    }

    $effect(() => {
        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    });
</script>

<div class="model-selector" bind:this={dropdownEl}>
    <button
        type="button"
        class="model-trigger"
        class:open={isOpen}
        class:disabled={disabled}
        onclick={toggle}
        title="Select model"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
            <path d="M2 4l6 5 6-5M2 4v8l6 5 6-5V4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span class="model-name" class:current={!!currentModel}>
            {currentModel || 'Select model'}
        </span>
        <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none">
            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>

    {#if isOpen}
        <div class="dropdown">
            {#if allModels.length === 0}
                <div class="empty">No providers configured</div>
            {:else}
                {#each allModels as provider}
                    <div class="provider-group">
                        <div class="provider-label">{provider.display_name || provider.name}</div>
                        {#each provider.models as model}
                            <button
                                type="button"
                                class="model-option"
                                class:active={model === currentModel && provider.url === currentUrl}
                                onclick={() => selectModel(provider, model)}
                            >
                                <span class="option-name">{model}</span>
                                {#if model === currentModel && provider.url === currentUrl}
                                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                                        <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                {/if}
                            </button>
                        {/each}
                    </div>
                {/each}
            {/if}
        </div>
    {/if}
</div>

<style>
    .model-selector {
        position: relative;
        flex-shrink: 0;
    }
    .model-trigger {
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
    .model-trigger:hover:not(.disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border-color: var(--color-accent);
    }
    .model-trigger.open {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
        color: var(--color-text-primary);
    }
    .model-trigger.disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .model-name {
        max-width: 120px;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .model-name.current {
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .chevron {
        transition: transform 0.15s ease;
    }
    .model-trigger.open .chevron {
        transform: rotate(180deg);
    }
    .dropdown {
        position: absolute;
        bottom: calc(100% + 6px);
        left: 0;
        min-width: 200px;
        max-height: 320px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.2);
        z-index: 50;
        padding: 4px;
    }
    .empty {
        padding: 12px 16px;
        font-size: 12px;
        color: var(--color-text-tertiary);
        text-align: center;
    }
    .provider-group {
        margin-bottom: 2px;
    }
    .provider-label {
        padding: 4px 8px 2px;
        font-size: 10px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-tertiary);
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
        background: var(--color-accent)18;
        color: var(--color-accent);
    }
    .option-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
</style>
