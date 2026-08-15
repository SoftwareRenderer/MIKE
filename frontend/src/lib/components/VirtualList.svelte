<script lang="ts">
    import type { Snippet } from 'svelte';

    /**
     * Fixed-height virtual scroll list component.
     *
     * Renders only visible rows plus a buffer, using absolute positioning
     * within a scrollable container. Lists below `minThreshold` items render
     * fully (no windowing). All rows must have the same height — use
     * `content-visibility: auto` (see ChatEntries.svelte) instead for
     * variable-height content.
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
        buffer = 5,
        onScrollElChange,
        role,
        'aria-label': ariaLabel,
        scrollTop: scrollTopProp,
        children,
        onscroll,
        onwheel,
        ontouchstart,
    }: {
        items: any[];
        itemHeight?: number;
        minThreshold?: number;
        buffer?: number;
        onScrollElChange?: (el: HTMLElement | null) => void;
        role?: string;
        'aria-label'?: string;
        scrollTop?: number;
        children: Snippet<[{ item: any; index: number }]>;
        onscroll?: () => void;
        onwheel?: () => void;
        ontouchstart?: () => void;
    } = $props();

    // Scroll container ref
    // svelte-ignore non_reactive_update
    let scrollEl: HTMLElement | null = null;

    // Keyboard navigation: Page Up/Down, Arrow Up/Down, Home, End
    function handleKeydown(e: KeyboardEvent) {
        // Skip if focus is inside an input/textarea/contenteditable — those keys move the cursor
        const target = e.target as HTMLElement;
        if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable) return;
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

    // Track internal scroll position and container height
    let internalScrollTop = $state(0);
    let containerHeight = $state(0);
    let scrollTop = $derived(scrollTopProp ?? internalScrollTop);

    // Notify parent when scroll element is available
    $effect(() => {
        if (onScrollElChange) {
            onScrollElChange(scrollEl);
        }
    });

    let virtualized = $derived(items.length >= minThreshold);
    let totalHeight = $derived(items.length * itemHeight);
    let startIndex = $derived(Math.max(0, Math.floor(scrollTop / itemHeight) - buffer));
    let endIndex = $derived(Math.min(items.length, Math.ceil((scrollTop + containerHeight) / itemHeight) + buffer));
    let visibleOffset = $derived(startIndex * itemHeight);

    function handleScroll() {
        if (scrollEl) internalScrollTop = scrollEl.scrollTop;
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

    /** Scroll to the bottom of the list. */
    function scrollToBottom() {
        if (scrollEl) {
            // Buffer past estimated height; browser clamps to actual max.
            scrollEl.scrollTop = totalHeight + 10000;
        }
    }

    /** Scroll to a specific offset. */
    function scrollToOffset(top: number, behavior: ScrollBehavior = 'auto') {
        if (scrollEl) {
            scrollEl.scrollTo({ top, behavior });
        }
    }

    /** Check if the scroll container is near the bottom. */
    function isNearBottom(threshold = 32): boolean {
        if (!scrollEl) return true;
        const gap = totalHeight - scrollEl.scrollTop - scrollEl.clientHeight;
        return gap < threshold;
    }

    /** Scroll to a specific item index. */
    function scrollToItem(index: number, behavior: ScrollBehavior = 'auto') {
        if (index < 0 || index >= items.length) return;
        scrollToOffset(index * itemHeight, behavior);
    }

    // Expose methods and state for parent access
    export { scrollEl as scrollElement, scrollToBottom, scrollToOffset, isNearBottom, scrollToItem };
</script>

{#if virtualized}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
        class="virtual-scroll"
        bind:this={scrollEl}
        role={role}
        aria-label={ariaLabel}
        tabindex="0"
        onscroll={onscroll}
        onwheel={onwheel}
        ontouchstart={ontouchstart}
        onkeydown={handleKeydown}
    >
        <div class="virtual-spacer" style="height: {totalHeight}px">
            <div class="virtual-content" style="transform: translateY({visibleOffset}px)">
                {#each items as item, idx}
                    {#if idx >= startIndex && idx < endIndex}
                        {@render children({ item, index: idx })}
                    {/if}
                {/each}
            </div>
        </div>
    </div>
{:else}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
        class="virtual-scroll"
        bind:this={scrollEl}
        role={role}
        aria-label={ariaLabel}
        tabindex="0"
        onscroll={onscroll}
        onwheel={onwheel}
        ontouchstart={ontouchstart}
        onkeydown={handleKeydown}
    >
        {#each items as item, index}
            {@render children({ item, index })}
        {/each}
    </div>
{/if}

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
        box-shadow: inset 0 0 0 1px var(--color-accent);
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
