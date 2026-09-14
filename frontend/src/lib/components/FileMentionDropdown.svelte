<script lang="ts">
    import { useClickOutside } from '$lib/composables/use-click-outside.svelte';

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

    // Compute final dropdown position with viewport overflow adjustment.
    // Anchored by BOTTOM edge so it stays pinned to the input as results
    // filter in/out (changing dropdown height). Position calculated ONCE
    // on open, then locked — no jumps on result updates.
    let dropdownBottom = $state(0);
    let dropdownLeft = $state(0);
    let dropdownWidth = $state(0);
    let placed = $state(false);
    const MIN_WIDTH = 260;
    const MAX_WIDTH = 400;

    $effect(() => {
        if (!position) {
            // Dropdown closed — reset for next open
            dropdownBottom = 0;
            dropdownLeft = 0;
            dropdownWidth = 0;
            placed = false;
            return;
        }
        if (!dropdownEl || results.length === 0) {
            return;
        }
        // If already placed, skip — prevents jumps when results update.
        if (placed) return;

        // Use requestAnimationFrame to measure after render
        const raf = requestAnimationFrame(() => {
            const rect = dropdownEl!.getBoundingClientRect();
            const viewportWidth = window.innerWidth;
            const viewportHeight = window.innerHeight;

            // Lock width to the input width, clamped to [MIN, MAX].
            const width = Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, position.width));
            dropdownWidth = width;

            // Anchor by bottom edge so it stays pinned as height changes.
            // CSS `bottom` is distance from viewport bottom, so convert viewport coords.
            // Default: show above input, bottom pinned to input's top edge.
            let bottom = viewportHeight - position.top;
            // If dropdown would overflow viewport top, show below input instead.
            if (position.top - rect.height < 8) {
                bottom = viewportHeight - position.bottom;
            }
            // Horizontal: constrain to viewport
            let left = position.left;
            if (left + width > viewportWidth - 8) {
                left = viewportWidth - width - 8;
            }
            if (left < 8) left = 8;

            dropdownBottom = bottom;
            dropdownLeft = left;
            placed = true;
        });
        return () => cancelAnimationFrame(raf);
    });

    function handleClick(path: string) {
        onSelect(path);
    }

    useClickOutside(() => dropdownEl, () => onClose(), { event: 'mousedown' });
</script>

{#if visible}
    <ul
        class="mention-dropdown"
        bind:this={dropdownEl}
        style="position:fixed; bottom:{dropdownBottom}px; left:{dropdownLeft}px; width:{dropdownWidth}px; z-index:1000;"
        role="listbox"
        aria-label="File suggestions"
        onclick={(e) => e.stopPropagation()}
        onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}
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
            width: calc(100vw - 16px) !important;
        }
    }
</style>
