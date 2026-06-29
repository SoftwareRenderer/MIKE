
<script lang="ts">
    import type { Snippet } from 'svelte';
    let { type, collapsible = false, summary = '', tokenCount = 0, children }: { 
        type: string; 
        collapsible?: boolean;
        summary?: string;
        tokenCount?: number;
        children?: Snippet;
    } = $props();

    let collapsed = $state(collapsible && !!summary);
    // Reactive references for prop values used in closures
    let collapsibleRef = $derived(collapsible);
    let summaryRef = $derived(summary);

    function iconForType(t: string): string {
        switch (t) {
            case 'text': return '💬';
            case 'reasoning': return '🧠';
            case 'tool_call': return '🔧';
            default: return '📄';
        }
    }

    function labelForType(t: string): string {
        switch (t) {
            case 'text': return 'Content';
            case 'reasoning': return 'Reasoning';
            case 'tool_call': return 'Tool Call';
            default: return '';
        }
    }

    function borderColor(t: string): string {
        switch (t) {
            case 'text': return 'var(--color-border)';
            case 'reasoning': return 'var(--color-status-purple)';
            case 'tool_call': return 'var(--color-accent)';
            default: return 'var(--color-border)';
        }
    }

    function toggle() {
        if (collapsible) collapsed = !collapsed;
    }
</script>

<div class="stream-section" style="border-left-color: {borderColor(type)}">
    {#if collapsible}
        <button class="section-header" onclick={toggle} aria-expanded={!collapsed}>
            <span class="icon">{iconForType(type)}</span>
            <span class="label">{labelForType(type)}</span>
            {#if tokenCount}
                <span class="token-count">({tokenCount} tokens)</span>
            {/if}
            <span class="collapse-icon" class:collapsed>{collapsed ? '▸' : '▾'}</span>
        </button>
        {#if collapsed}
            <div class="collapsed-summary">{summary || 'Thinking...'}</div>
        {:else}
            {@render children?.()}
        {/if}
    {:else}
        {@render children?.()}
    {/if}
</div>

<style>
    .stream-section {
        border-left: 3px solid;
        padding: 4px 8px;
        margin: 4px 0;
    }
    .section-header {
        display: flex;
        align-items: center;
        gap: 6px;
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-weight: 500;
        padding: 4px 0;
        cursor: pointer;
        width: 100%;
        text-align: left;
    }
    .section-header:hover {
        color: var(--color-text-primary);
    }
    .icon { font-size: 0.875rem; }
    .label { text-transform: capitalize; }
    .token-count {
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }
    .collapse-icon {
        margin-left: auto;
        transition: transform 0.2s;
    }
    .collapse-icon.collapsed {
        transform: rotate(-90deg);
    }
    .collapsed-summary {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-style: italic;
        padding: 2px 0 4px;
    }
</style>