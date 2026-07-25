<script lang="ts">
    import type { Provider } from '$lib/types';
    import { PI_SENTINEL_URL } from '$lib/types';

    let {
        providers,
        currentUrl,
        currentModel,
        currentEffort,
        rect,
        onSelectAgent,
        onSelectModel,
    }: {
        providers: Provider[];
        currentUrl: string | undefined;
        currentModel: string | undefined;
        currentEffort: string | undefined;
        rect: { top: number; left: number } | null;
        onSelectAgent: () => void;
        onSelectModel: (url: string, model: string, effort: string) => void;
    } = $props();

    let isAgentActive = $derived(currentUrl === PI_SENTINEL_URL);
    let tag = $derived('div');
</script>

<svelte:element this={tag} class="portal-card-model-picker" role="listbox" aria-label="Select model" style="position:fixed;top:{rect?.top ?? 0}px;left:{rect?.left ?? 0}px;z-index:10000;min-width:200px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
    <button type="button" class="picker-model-option picker-agent-option" class:active={isAgentActive} onclick={onSelectAgent} role="option" aria-selected={isAgentActive}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" style="margin-right:6px;flex-shrink:0">
            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
            <circle cx="6" cy="9" r="1" fill="currentColor"/>
            <circle cx="10" cy="9" r="1" fill="currentColor"/>
        </svg>
        <span class="option-name">Pi Coding Agent</span>
        {#if isAgentActive}
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
        {/if}
    </button>
    {#each providers as provider (provider.name)}
        {#if provider.models.length > 0}
            {@const provUrl = provider.url}
            {@const pairs = (() => {
                const seen = new Set<string>();
                const result: Array<{name: string; effort: string}> = [];
                for (const m of provider.models) {
                    const name = typeof m === 'string' ? m : m.name;
                    const effort = (typeof m === 'object' && m.reasoning_effort) ? m.reasoning_effort : 'medium';
                    const key = `${name}|${effort}`;
                    if (!seen.has(key) && name.trim()) {
                        seen.add(key);
                        result.push({ name, effort });
                    }
                }
                return result;
            })()}
            <div class="picker-provider-header">{provider.display_name || provider.name}</div>
            {#each pairs as m (m.name + '|' + m.effort)}
                {@const isActive = m.name === currentModel && m.effort === (currentEffort || 'medium')}
                <button type="button" class="picker-model-option" class:active={isActive} onclick={() => onSelectModel(provUrl, m.name, m.effort)} role="option" aria-selected={isActive}>
                    <span class="option-name">{m.name}</span>
                    {#if m.effort && m.effort !== 'medium'}
                        <span class="effort-badge">{m.effort}</span>
                    {/if}
                    {#if isActive}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {/if}
                </button>
            {/each}
        {/if}
    {/each}
</svelte:element>

<style>
    .picker-agent-option {
        font-weight: 600;
        border-bottom: 1px solid var(--color-border);
        margin-bottom: 4px;
        padding-bottom: 6px;
    }
    .picker-model-option {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 8px;
        border-radius: 4px;
        background: none;
        border: none;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        text-align: left;
        font-family: inherit;
    }
    .picker-model-option:hover {
        background: var(--color-surface-3);
    }
    .picker-model-option.active {
        background: var(--color-accent)11;
        color: var(--color-accent);
    }
    .option-name {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .effort-badge {
        font-size: 0.6875rem;
        padding: 1px 5px;
        border-radius: 4px;
        background: var(--color-surface-4);
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }
    .picker-provider-header {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-tertiary);
        padding: 8px 8px 2px;
    }
</style>
