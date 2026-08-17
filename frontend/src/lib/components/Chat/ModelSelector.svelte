<script lang="ts">
    import type { Provider } from '$lib/types';
    import { AGENT_URL_PREFIX } from '$lib/types';
    import { getModelName, getProviderDisplayName, providerRef, displayModelName } from '$lib/utils/providers';

    interface Props {
        providers: Provider[];
        currentModel: string;
        currentUrl: string;
        currentReasoningEffort?: string;
        disabled?: boolean;
        onSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
    }

    let { providers, currentModel, currentUrl, currentReasoningEffort = '', disabled = false, onSelect }: Props = $props();

    let selectedProviderUrl = $state('');
    let providerDropdownOpen = $state(false);
    let modelDropdownOpen = $state(false);

    $effect(() => {
        selectedProviderUrl = currentUrl || '';
    });

    let providerTriggerEl: HTMLButtonElement | null = $state(null);
    let modelTriggerEl: HTMLButtonElement | null = $state(null);

    let providerRect = $state<{ top: number; left: number } | null>(null);
    let modelRect = $state<{ top: number; left: number } | null>(null);

    let selectedProvider = $derived(
        providers.find(p => p.url === selectedProviderUrl) ||
        providers.find(p => p.url === currentUrl) ||
        providers[0] ||
        null
    );

    /** Resolve { name, effort } for a model entry (string or object). */
    function resolveModelInfo(m: string | { name: string; reasoning_effort?: string }): { name: string; effort: string } {
        return {
            name: typeof m === 'string' ? m : m.name,
            effort: (typeof m === 'object' && m.reasoning_effort) ? m.reasoning_effort : 'default',
        };
    }

    let availableModels = $derived(
        (() => {
            if (!selectedProvider?.models.length) return [];
            // Build unique model+effort pairs
            const pairs = new Map<string, { name: string; effort: string }>();
            for (const m of selectedProvider.models) {
                const info = resolveModelInfo(m);
                if (!info.name.trim()) continue;
                const key = `${info.name}|${info.effort}`;
                if (!pairs.has(key)) {
                    pairs.set(key, info);
                }
            }
            return Array.from(pairs.values())
                .toSorted((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }) || a.effort.localeCompare(b.effort));
        })()
    );

    let activeProviders = $derived(
        providers.filter(p => p.models.length > 0)
    );

    let hasAgentProvider = $derived(
        providers.some(p => p.kind === 'agent' || p.url.startsWith(AGENT_URL_PREFIX))
    );

    let agentProvider = $derived(
        providers.find(p => p.kind === 'agent' || p.url.startsWith(AGENT_URL_PREFIX)) || null
    );

    // Total selectable options: LLM providers with models + agent
    let selectableCount = $derived(
        activeProviders.length + (hasAgentProvider ? 1 : 0)
    );

    let isAgentSelected = $derived(
        selectedProviderUrl.startsWith(AGENT_URL_PREFIX)
    );

    // Agent model fetching
    let agentModels = $state<Array<{ name: string; context_window?: number }>>([]);
    let agentModelsLoading = $state(false);
    let lastFetchedAgentUrl = $state('');

    async function fetchAgentModels(url: string) {
        if (!url.startsWith(AGENT_URL_PREFIX) || agentModelsLoading || url === lastFetchedAgentUrl) return;
        lastFetchedAgentUrl = url;
        agentModelsLoading = true;
        agentModels = [];
        try {
            const { fetchModels } = await import('$lib/api');
            const result = await fetchModels(url);
            if (result.models && result.models.length > 0) {
                agentModels = result.models;
            }
        } catch (e) {
            console.error('Failed to fetch agent models:', e);
        } finally {
            agentModelsLoading = false;
        }
    }

    // Fetch agent models when selectedProviderUrl changes to an agent URL
    $effect(() => {
        const url = selectedProviderUrl;
        if (url.startsWith(AGENT_URL_PREFIX)) {
            fetchAgentModels(url);
        } else {
            agentModels = [];
            lastFetchedAgentUrl = '';
        }
    });

    $effect(() => {
        if (currentModel && providers.length > 0) {
            // Only override the provider URL if the current provider (from currentUrl)
            // doesn't have the model. This avoids conflicts when multiple providers
            // share the same model name.
            const currentProvider = providers.find(p => p.url === currentUrl);
            const hasModel = currentProvider?.models.some(m => (typeof m === 'string' ? m : m.name) === currentModel);
            if (!hasModel) {
                const providerWithModel = providers.find(p => p.models.some(m => (typeof m === 'string' ? m : m.name) === currentModel));
                if (providerWithModel && providerWithModel.url !== selectedProviderUrl) {
                    selectedProviderUrl = providerWithModel.url;
                }
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
        let modelToSelect: { name: string; effort: string } | null = null;
        if (provider.models.length > 0) {
            modelToSelect = resolveModelInfo(provider.models[0]);
        }
        if (modelToSelect) {
            onSelect(providerRef(provider), modelToSelect.name, provider.url, modelToSelect.effort);
        }
    }

    function selectAgent() {
        const agentProvider = providers.find(p => p.kind === 'agent' || p.url.startsWith(AGENT_URL_PREFIX));
        if (!agentProvider) return;
        selectedProviderUrl = agentProvider.url;
        providerDropdownOpen = false;
        modelDropdownOpen = false;
        // If agent has static models, select one immediately
        if (agentProvider.models.length > 0) {
            onSelect(providerRef(agentProvider), getModelName(agentProvider.models[0]), agentProvider.url);
        } else {
            // No static models — call onSelect with placeholder so parent updates selectedUrl.
            // Real model auto-selected by $effect below when dynamic models are fetched.
            onSelect(providerRef(agentProvider), getProviderDisplayName(agentProvider, providers), agentProvider.url);
        }
        // Trigger agent model fetch — auto-select handled by $effect below
        fetchAgentModels(agentProvider.url);
    }

    // Auto-select a model when agent models are fetched
    $effect(() => {
        if (agentModels.length > 0 && isAgentSelected) {
            const agentProvider = providers.find(p => p.url === selectedProviderUrl);
            if (!agentProvider) return;
            // Fallback: first fetched model (only if nothing valid selected)
            if (!currentModel || currentModel === getProviderDisplayName(agentProvider, providers)) {
                onSelect(providerRef(agentProvider), agentModels[0].name, agentProvider.url);
            }
        }
    });

    function selectModel(modelName: string, reasoningEffort?: string) {
        if (!selectedProvider) return;
        onSelect(providerRef(selectedProvider), modelName, selectedProvider.url, reasoningEffort);
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
    {#if selectableCount > 1}
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
                {isAgentSelected ? (selectedProvider ? getProviderDisplayName(selectedProvider, providers) : 'Agent') : (selectedProvider ? getProviderDisplayName(selectedProvider, providers) : 'Provider')}
            </span>
            <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    {/if}

    {#if !isAgentSelected || agentModelsLoading || agentModels.length > 0}
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
                {isAgentSelected && agentModelsLoading ? 'Loading...' : (currentModel ? displayModelName(currentModel) : 'Select model')}
            </span>
            <svg class="chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    {/if}
</div>

<!-- Provider dropdown (rendered in shadow DOM to escape overflow clipping) -->
{#if providerDropdownOpen && providerRect}
    <svelte:element this={"div"} class="portal-provider-dropdown" role="listbox" aria-label="Select provider" style="position:fixed;top:{providerRect.top}px;left:{providerRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#if hasAgentProvider}
            <button type="button" class="provider-option provider-option-agent" class:active={isAgentSelected} onclick={selectAgent} role="option" aria-selected={isAgentSelected}>
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                    <circle cx="6" cy="9" r="1" fill="currentColor"/>
                    <circle cx="10" cy="9" r="1" fill="currentColor"/>
                </svg>
                <span class="provider-name">{agentProvider ? getProviderDisplayName(agentProvider, providers) : 'Agent'}</span>
                {#if isAgentSelected}
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                {/if}
            </button>
        {/if}
        {#each activeProviders as provider}
            <button type="button" class="provider-option" class:active={provider.url === selectedProviderUrl} onclick={() => selectProvider(provider)} role="option" aria-selected={provider.url === selectedProviderUrl}>
                <span class="provider-name">{getProviderDisplayName(provider, providers)}</span>
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
    <svelte:element this={"div"} class="portal-model-dropdown" role="listbox" aria-label="Select model" style="position:fixed;top:{modelRect.top}px;left:{modelRect.left}px;z-index:10000;min-width:180px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#if isAgentSelected}
            {#if agentModelsLoading}
                <div class="empty">Loading models...</div>
            {:else if agentModels.length === 0}
                <div class="empty">No models available</div>
            {:else}
                {#each agentModels as m}
                    {@const isActive = m.name === currentModel}
                    <button type="button" class="model-option" class:active={isActive} onclick={() => selectModel(m.name)} role="option" aria-selected={isActive}>
                        <span class="option-name">{m.name}</span>
                        {#if isActive}
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        {/if}
                    </button>
                {/each}
            {/if}
        {:else if availableModels.length === 0}
            <div class="empty">No models available</div>
        {:else}
            {#each availableModels as m}
                {@const isActive = m.name === currentModel && m.effort === (currentReasoningEffort || 'default')}
                <button type="button" class="model-option" class:active={isActive} onclick={() => selectModel(m.name, m.effort)} role="option" aria-selected={isActive}>
                    <span class="option-name">{m.name}</span>
                    {#if m.effort && m.effort !== 'default'}
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
        flex-wrap: wrap;
        gap: 4px;
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
    .provider-option-agent {
        color: var(--color-status-purple);
    }
    .provider-option-agent:hover {
        background: rgba(163, 113, 247, 0.08);
    }
    .provider-option-agent.active {
        background: rgba(163, 113, 247, 0.1);
        color: var(--color-status-purple);
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
