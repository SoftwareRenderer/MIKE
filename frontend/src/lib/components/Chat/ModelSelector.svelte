<script lang="ts">
    import type { Provider } from '$lib/types';

    interface Props {
        providers: Provider[];
        currentModel: string;
        currentUrl: string;
        currentReasoningEffort?: string;
        disabled?: boolean;
        onSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
    }

    let { providers, currentModel, currentUrl, currentReasoningEffort = '', disabled = false, onSelect }: Props = $props();

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
        (() => {
            if (!selectedProvider?.models.length) return [];
            // Build unique model+effort pairs
            const pairs = new Map<string, { name: string; effort: string }>();
            for (const m of selectedProvider.models) {
                const name = typeof m === 'string' ? m : m.name;
                const effort = (typeof m === 'object' && m.reasoning_effort) ? m.reasoning_effort : 'medium';
                if (!name.trim()) continue;
                const key = `${name}|${effort}`;
                if (!pairs.has(key)) {
                    pairs.set(key, { name, effort });
                }
            }
            return Array.from(pairs.values())
                .toSorted((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }) || a.effort.localeCompare(b.effort));
        })()
    );

    let activeProviders = $derived(
        providers.filter(p => p.models.length > 0)
    );

    $effect(() => {
        selectedProviderUrl = currentUrl || '';
    });

    $effect(() => {
        if (currentModel && providers.length > 0) {
            const providerWithModel = providers.find(p => p.models.some(m => (typeof m === 'string' ? m : m.name) === currentModel));
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

    // After dropdown renders, measure actual size and adjust if it would overflow
    function adjustDropdownPosition(portalClass: string, triggerEl: HTMLElement | null, rect: { top: number; left: number } | null, setRect: (r: { top: number; left: number } | null) => void) {
        if (!triggerEl || !rect) return;
        const portal = document.querySelector(portalClass);
        if (!portal) return;
        const triggerTop = triggerEl.getBoundingClientRect().top;
        const dropdownHeight = portal.getBoundingClientRect().height;
        const dropdownWidth = portal.getBoundingClientRect().width;
        const viewportHeight = window.visualViewport?.height || window.innerHeight;
        const viewportWidth = window.visualViewport?.width || window.innerWidth;
        let top = rect.top;
        let left = rect.left;
        // Flip above if not enough space below
        const spaceBelow = viewportHeight - (triggerTop + triggerEl.getBoundingClientRect().height);
        if (spaceBelow < dropdownHeight + 12) {
            top = triggerTop - dropdownHeight - 6;
        }
        // Shift left if overflowing right edge
        if (left + dropdownWidth > viewportWidth - 8) {
            left = viewportWidth - dropdownWidth - 8;
        }
        // Ensure left edge doesn't go negative
        if (left < 8) left = 8;
        setRect({ top, left });
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
        const getModelInfo = (m: string | { name: string; reasoning_effort?: string }) => ({
            name: typeof m === 'string' ? m : m.name,
            effort: (typeof m === 'object' && m.reasoning_effort) ? m.reasoning_effort : 'medium',
        });
        let modelToSelect: { name: string; effort: string } | null = null;
        if (provider.default_model) {
            // Parse composite default: modelName|effort
            const parts = provider.default_model.split('|');
            const defaultName = parts[0];
            const defaultEffort = parts[1] || 'medium';
            const match = provider.models.map(getModelInfo).find(m => m.name === defaultName && m.effort === defaultEffort);
            // Fallback: match by name only (backward compat)
            if (!match) {
                const nameOnly = provider.models.map(getModelInfo).find(m => m.name === defaultName);
                if (nameOnly) modelToSelect = nameOnly;
            } else {
                modelToSelect = match;
            }
        }
        if (!modelToSelect && provider.models.length > 0) {
            modelToSelect = getModelInfo(provider.models[0]);
        }
        if (modelToSelect) {
            onSelect(provider.name, modelToSelect.name, provider.url, modelToSelect.effort);
        }
    }

    function selectModel(modelName: string, reasoningEffort?: string) {
        if (!selectedProvider) return;
        onSelect(selectedProvider.name, modelName, selectedProvider.url, reasoningEffort);
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

    function handleProviderKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            providerDropdownOpen = false;
            providerTriggerEl?.focus();
            return;
        }
        if (!providerDropdownOpen && (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault();
            providerDropdownOpen = true;
            modelDropdownOpen = false;
            return;
        }
        if (providerDropdownOpen && e.key === 'ArrowDown') {
            e.preventDefault();
            const options = document.querySelectorAll('.portal-provider-dropdown [role="option"]');
            const current = document.activeElement as HTMLElement;
            const idx = Array.from(options).indexOf(current as never);
            const next = options[(idx + 1) % options.length] as HTMLElement;
            next?.focus();
        }
        if (providerDropdownOpen && e.key === 'ArrowUp') {
            e.preventDefault();
            const options = document.querySelectorAll('.portal-provider-dropdown [role="option"]');
            const current = document.activeElement as HTMLElement;
            const idx = Array.from(options).indexOf(current as never);
            const prev = options[(idx - 1 + options.length) % options.length] as HTMLElement;
            prev?.focus();
        }
    }

    function handleModelKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            modelDropdownOpen = false;
            modelTriggerEl?.focus();
            return;
        }
        if (!modelDropdownOpen && (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ')) {
            e.preventDefault();
            modelDropdownOpen = true;
            providerDropdownOpen = false;
            return;
        }
        if (modelDropdownOpen && e.key === 'ArrowDown') {
            e.preventDefault();
            const options = document.querySelectorAll('.portal-model-dropdown [role="option"]');
            const current = document.activeElement as HTMLElement;
            const idx = Array.from(options).indexOf(current as never);
            const next = options[(idx + 1) % options.length] as HTMLElement;
            next?.focus();
        }
        if (modelDropdownOpen && e.key === 'ArrowUp') {
            e.preventDefault();
            const options = document.querySelectorAll('.portal-model-dropdown [role="option"]');
            const current = document.activeElement as HTMLElement;
            const idx = Array.from(options).indexOf(current as never);
            const prev = options[(idx - 1 + options.length) % options.length] as HTMLElement;
            prev?.focus();
        }
    }

    $effect(() => {
        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    });

    // Measure and adjust dropdown positions after render
    $effect(() => {
        if (providerDropdownOpen && providerRect) {
            const raf = requestAnimationFrame(() => {
                adjustDropdownPosition('.portal-provider-dropdown', providerTriggerEl, providerRect, (r) => { providerRect = r; });
            });
            return () => cancelAnimationFrame(raf);
        }
    });

    $effect(() => {
        if (modelDropdownOpen && modelRect) {
            const raf = requestAnimationFrame(() => {
                adjustDropdownPosition('.portal-model-dropdown', modelTriggerEl, modelRect, (r) => { modelRect = r; });
            });
            return () => cancelAnimationFrame(raf);
        }
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
            onkeydown={handleProviderKeydown}
            aria-label="Select provider"
            aria-haspopup="listbox"
            aria-expanded={providerDropdownOpen}
            title="Select provider"
        >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="2" y="2" width="12" height="12" rx="2" stroke="currentColor" stroke-width="1.5"/>
                <path d="M6 8h4M8 6v4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
            <span class="selector-label">
                {selectedProvider?.display_name || selectedProvider?.name || 'Provider'}
            </span>
            <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
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
        onkeydown={handleModelKeydown}
        aria-label="Select model"
        aria-haspopup="listbox"
        aria-expanded={modelDropdownOpen}
        title="Select model"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M2 5l6-3 6 3v6l-6 3-6-3V5z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
            <line x1="8" y1="8" x2="8" y2="14" stroke="currentColor" stroke-width="1.5"/>
            <line x1="2" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
            <line x1="14" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
        </svg>
        <span class="selector-label model-name" class:current={!!currentModel}>
            {currentModel || 'Select model'}
        </span>
        <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>
</div>

<!-- Provider dropdown (rendered in shadow DOM to escape overflow clipping) -->
{#if providerDropdownOpen && providerRect}
    <svelte:element this="div" class="portal-provider-dropdown" role="listbox" aria-label="Select provider" style="position:fixed;top:{providerRect.top}px;left:{providerRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#each activeProviders as provider}
            <button type="button" class="provider-option" class:active={provider.url === selectedProviderUrl} onclick={() => selectProvider(provider)} role="option" aria-selected={provider.url === selectedProviderUrl}>
                <span class="provider-name">{provider.display_name || provider.name}</span>
                <span class="model-count">{provider.models.length} model{provider.models.length !== 1 ? 's' : ''}</span>
                {#if provider.url === selectedProviderUrl}
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                {/if}
            </button>
        {/each}
    </svelte:element>
{/if}

<!-- Model dropdown -->
{#if modelDropdownOpen && modelRect}
    <svelte:element this="div" class="portal-model-dropdown" role="listbox" aria-label="Select model" style="position:fixed;top:{modelRect.top}px;left:{modelRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#if availableModels.length === 0}
            <div class="empty">No models available</div>
        {:else}
            {#each availableModels as m}
                {@const isActive = m.name === currentModel && m.effort === (currentReasoningEffort || 'medium')}
                <button type="button" class="model-option" class:active={isActive} onclick={() => selectModel(m.name, m.effort)} role="option" aria-selected={isActive}>
                    <span class="option-name">{m.name}</span>
                    {#if m.effort && m.effort !== 'medium'}
                        <span class="effort-badge">{m.effort}</span>
                    {/if}
                    {#if isActive}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
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
        font-size: 0.75rem;
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
        font-size: 0.75rem;
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
        font-size: 0.75rem;
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
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }

    .model-option {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        border: none;
        background: none;
        color: var(--color-text-primary);
        font-size: 0.75rem;
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
    .effort-badge {
        font-size: 0.65rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 1px 5px;
        border-radius: 3px;
        flex-shrink: 0;
        margin-left: 4px;
    }
</style>
