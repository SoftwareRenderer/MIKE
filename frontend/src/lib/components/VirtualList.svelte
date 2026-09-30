<script lang="ts">
    import type { Snippet } from 'svelte';
    import { isEditableTarget } from '$lib/utils/keyboard';

    /**
     * Fixed-height virtual scroll list: renders visible rows plus a buffer, absolutely
     * positioned in a scrollable container. Lists below `minThreshold` items render fully.
     * All rows must be the same height — variable-height content wants
     * `content-visibility: auto` (see ChatEntries.svelte) instead.
     *
     * Usage:
     *   <VirtualList {items} itemHeight={28} minThreshold={50}
     *     onScrollElChange={(el) => scrollEl = el}>
     *     {#snippet children({ item, index })}
     *       <div>{item.name}</div>
     *     {/snippet}
     *   </VirtualList>
     */
    let {
        items,
        itemHeight = 28,
        minThreshold = 50,
        onScrollElChange,
        role,
        'aria-label': ariaLabel,
        children,
        onscroll,
    }: {
        items: any[];
        itemHeight?: number;
        minThreshold?: number;
        onScrollElChange?: (el: HTMLElement | null) => void;
        role?: string;
        'aria-label'?: string;
        children: Snippet<[{ item: any; index: number }]>;
        onscroll?: () => void;
    } = $props();

    // Rows rendered beyond the viewport on each side.
    const ROW_BUFFER = 5;

    // Scroll container ref
    // svelte-ignore non_reactive_update
    let scrollEl: HTMLElement | null = null;

    // Keyboard navigation: Page Up/Down, Arrow Up/Down, Home, End
    function handleKeydown(e: KeyboardEvent) {
        // Skip if focus is inside an input/textarea/contenteditable — those keys move the cursor
        if (isEditableTarget(e.target)) return;
        const el = scrollEl;
        if (!el) return;
        const scrollAmount = e.key === 'ArrowUp' || e.key === 'ArrowDown' ? 80 : el.clientHeight * 0.8;
        if (e.key === 'ArrowDown' || e.key === 'PageDown') {
            e.preventDefault();
            el.scrollBy({ top: scrollAmount, behavior: 'smooth' });
        } else if (e.key === 'ArrowUp' || e.key === 'PageUp') {
            e.preventDefault();
            el.scrollBy({ top: -scrollAmount, behavior: 'smooth' });
        } else if (e.key === 'Home') {
            e.preventDefault();
            el.scrollTo({ top: 0, behavior: 'smooth' });
        } else if (e.key === 'End') {
            e.preventDefault();
            el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
        }
    }

    // Scroll position and container height, read from the container's own events
    let scrollTop = $state(0);
    let containerHeight = $state(0);

    // Notify parent when scroll element is available
    $effect(() => {
        if (onScrollElChange) {
            onScrollElChange(scrollEl);
        }
    });

    let virtualized = $derived(items.length >= minThreshold);
    let totalHeight = $derived(items.length * itemHeight);
    let startIndex = $derived(Math.max(0, Math.floor(scrollTop / itemHeight) - ROW_BUFFER));
    let endIndex = $derived(Math.min(items.length, Math.ceil((scrollTop + containerHeight) / itemHeight) + ROW_BUFFER));
    let visibleOffset = $derived(startIndex * itemHeight);

    function handleScroll() {
        if (scrollEl) scrollTop = scrollEl.scrollTop;
    }

    function handleResize() {
        if (scrollEl) containerHeight = scrollEl.clientHeight;
    }

    $effect(() => {
        if (scrollEl) {
            containerHeight = scrollEl.clientHeight;
            scrollEl.addEventListener('scroll', handleScroll, { passive: true });
            window.addEventListener('resize', handleResize, { passive: true });
            return () => {
                scrollEl?.removeEventListener('scroll', handleScroll);
                window.removeEventListener('resize', handleResize);
            };
        }
    });

</script>

<!-- One scroll container: crossing minThreshold changes the rows, not the element. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
    class="virtual-scroll"
    bind:this={scrollEl}
    role={role}
    aria-label={ariaLabel}
    tabindex="0"
    onscroll={onscroll}
    onkeydown={handleKeydown}
>
    {#if virtualized}
        <div class="virtual-spacer" style="height: {totalHeight}px">
            <div class="virtual-content" style="transform: translateY({visibleOffset}px)">
                {#each items as item, idx}
                    {#if idx >= startIndex && idx < endIndex}
                        {@render children({ item, index: idx })}
                    {/if}
                {/each}
            </div>
        </div>
    {:else}
        {#each items as item, index}
            {@render children({ item, index })}
        {/each}
    {/if}
</div>

<style>
    .virtual-scroll {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
        overflow-y: auto;
        overflow-x: hidden;
        outline: none;
    }
    .virtual-scroll:focus-visible {
        box-shadow: var(--focus-ring);
    }

    .virtual-spacer {
        position: relative;
        width: 100%;
        flex-shrink: 0;
    }
    .virtual-content {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
    }
</style>
