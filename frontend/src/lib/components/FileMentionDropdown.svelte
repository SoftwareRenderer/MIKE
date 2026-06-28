<script lang="ts">
    import { onMount, onDestroy } from 'svelte';

    interface Props {
        results: Array<{ path: string }>;
        position: { top: number; bottom: number; left: number; width: number } | null;
        activeIndex: number;
        onSelect: (path: string) => void;
        onClose: () => void;
    }

    let { results, position, activeIndex, onSelect, onClose }: Props = $props();

    let dropdownEl: HTMLUListElement | null = $state(null);
    let visible = $derived(position !== null && results.length > 0);

    // Compute final dropdown position with viewport overflow adjustment
    let dropdownTop = $derived(0);
    let dropdownLeft = $derived(0);

    $effect(() => {
        if (!visible || !dropdownEl || !position) {
            dropdownTop = 0;
            dropdownLeft = 0;
            return;
        }
        // Use requestAnimationFrame to measure after render
        const raf = requestAnimationFrame(() => {
            const rect = dropdownEl!.getBoundingClientRect();
            const viewportHeight = window.innerHeight;
            const viewportWidth = window.innerWidth;

            // Position above the input area
            let top = position.top - rect.height - 4;
            let left = position.left;

            // Adjust vertical: show below input if it would overflow top
            if (top < 8) {
                top = position.bottom + 4;
            }
            // Adjust horizontal: constrain to viewport
            if (left + rect.width > viewportWidth - 8) {
                left = viewportWidth - rect.width - 8;
            }
            if (left < 8) left = 8;

            dropdownTop = top;
            dropdownLeft = left;
        });
        return () => cancelAnimationFrame(raf);
    });

    function handleClick(path: string) {
        onSelect(path);
    }

    // Close on outside click
    let handleClickOutside: ((e: MouseEvent) => void) | null = null;
    onMount(() => {
        handleClickOutside = (e: MouseEvent) => {
            if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
                onClose();
            }
        };
        document.addEventListener('mousedown', handleClickOutside);
    });
    onDestroy(() => {
        if (handleClickOutside) {
            document.removeEventListener('mousedown', handleClickOutside);
        }
    });
</script>

{#if visible}
    <ul
        class="mention-dropdown"
        bind:this={dropdownEl}
        style="position:fixed; top:{dropdownTop}px; left:{dropdownLeft}px; z-index:1000;"
        role="listbox"
        aria-label="File suggestions"
    >
        {#each results as result, i}
            <li
                class="mention-item"
                class:active={i === activeIndex}
                role="option"
                aria-selected={i === activeIndex}
                tabindex="-1"
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') e.preventDefault(); }}
                onclick={() => handleClick(result.path)}
            >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
                    <polyline points="14,2 14,8 20,8"/>
                </svg>
                <span class="mention-path">{result.path}</span>
            </li>
        {/each}
    </ul>
{/if}

<style>
    .mention-dropdown {
        min-width: 260px;
        max-width: 400px;
        max-height: 240px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 16px rgba(0,0,0,0.2);
        padding: 4px;
        margin: 0;
        list-style: none;
    }
    .mention-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        cursor: pointer;
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-secondary);
        transition: background 0.1s ease, color 0.1s ease;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .mention-item:hover,
    .mention-item.active {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .mention-item.active {
        outline: 1px solid var(--color-accent)44;
    }
    .mention-item svg {
        flex-shrink: 0;
        opacity: 0.5;
    }
    .mention-path {
        overflow: hidden;
        text-overflow: ellipsis;
    }

    @media (max-width: 768px) {
        .mention-dropdown {
            min-width: 200px;
            max-width: calc(100vw - 16px);
        }
    }
</style>
