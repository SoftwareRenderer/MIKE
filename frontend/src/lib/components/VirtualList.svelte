<script lang="ts">
    import type { Snippet } from 'svelte';
    import { useVirtualScroll } from '$lib/composables/use-virtual-scroll.svelte';

    /**
     * Generic virtual scroll list component.
     *
     * Renders only visible items plus an overscan buffer, using absolute
     * positioning within a scrollable container. Items are automatically
     * measured via ResizeObserver on their wrapper elements.
     *
     * Full API (for variable-height items):
     *   <VirtualList {items} itemHeight={getEstimatedHeight} getKey={(i) => i.id}
     *     onScrollElChange={(el) => scrollEl = el} overscan={5}>
     *     {#snippet children({ item, index, offset })}
     *       <div>{item.content}</div>
     *     {/snippet}
     *   </VirtualList>
     *
     * Legacy API (for fixed-height items, backward compatible):
     *   <VirtualList {items} itemHeight={28} minThreshold={50}>
     *     {#snippet children({ item, index })}
     *       <div>{item.name}</div>
     *     {/snippet}
     *   </VirtualList>
     */
    let {
        items,
        itemHeight = 28,
        minThreshold = 50,
        overscan = 3,
        buffer = 5,
        getKey,
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
        itemHeight?: number | ((item: any, index: number) => number);
        minThreshold?: number;
        overscan?: number;
        buffer?: number;
        getKey?: (item: any) => string;
        onScrollElChange?: (el: HTMLElement | null) => void;
        role?: string;
        'aria-label'?: string;
        scrollTop?: number;
        children: Snippet<[{ item: any; index: number; offset?: number; measure?: (el: HTMLElement | null) => void }]>;
        onscroll?: () => void;
        onwheel?: () => void;
        ontouchstart?: () => void;
    } = $props();

    // Determine if we're using the legacy (fixed-height) or new (variable-height) API
    let isLegacyApi = $derived(!getKey && typeof itemHeight === 'number');
    let effectiveOverscan = $derived(isLegacyApi ? buffer : overscan);

    // Scroll container ref
    let scrollEl: HTMLElement | null = null;

    // Keyboard navigation: Page Up/Down, Arrow Up/Down, Home, End
    function handleKeydown(e: KeyboardEvent) {
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

    // Legacy: track internal scroll position and container height
    let internalScrollTop = $state(0);
    let containerHeight = $state(0);
    let scrollTop = $derived(scrollTopProp ?? internalScrollTop);

    // Notify parent when scroll element is available
    $effect(() => {
        if (onScrollElChange) {
            onScrollElChange(scrollEl);
        }
    });

    // ── Legacy mode (fixed-height, minThreshold) ──

    let virtualized = $derived(items.length >= minThreshold);
    let totalHeight = $derived(items.length * (typeof itemHeight === 'number' ? itemHeight : 28));
    let startIndex = $derived(Math.max(0, Math.floor(scrollTop / (typeof itemHeight === 'number' ? itemHeight : 28)) - effectiveOverscan));
    let endIndex = $derived(Math.min(items.length, Math.ceil((scrollTop + containerHeight) / (typeof itemHeight === 'number' ? itemHeight : 28)) + effectiveOverscan));
    let visibleOffset = $derived(startIndex * (typeof itemHeight === 'number' ? itemHeight : 28));

    function handleScroll() {
        if (scrollEl) internalScrollTop = scrollEl.scrollTop;
    }

    function handleResize() {
        if (scrollEl) containerHeight = scrollEl.clientHeight;
    }

    // ── New mode (variable-height, useVirtualScroll) ──

    // Virtual scroll controller (only used in non-legacy mode)
    let vs: ReturnType<typeof useVirtualScroll> | null = null;

    if (!isLegacyApi) {
        vs = useVirtualScroll(
            () => scrollEl,
            () => items.length,
            (index) => {
                if (index < 0 || index >= items.length) return 60;
                return typeof itemHeight === 'function'
                    ? itemHeight(items[index], index)
                    : itemHeight;
            },
            effectiveOverscan,
        );
    }

    // Initialize container height for legacy mode
    $effect(() => {
        if (scrollEl && isLegacyApi) {
            containerHeight = scrollEl.clientHeight;
            scrollEl.addEventListener('scroll', handleScroll, { passive: true });
            window.addEventListener('resize', handleResize, { passive: true });
            return () => {
                scrollEl?.removeEventListener('scroll', handleScroll);
                window.removeEventListener('resize', handleResize);
            };
        }
    });

    // New mode: visible items with offsets
    let newVisibleItems = $derived.by(() => {
        if (!vs || isLegacyApi) return [];
        const result: Array<{ item: any; index: number; offset: number }> = [];
        const end = Math.min(vs.endIdx, items.length);
        for (let i = vs.startIdx; i < end; i++) {
            result.push({
                item: items[i],
                index: i,
                offset: vs.getItemOffset(i),
            });
        }
        return result;
    });

    // New mode: ResizeObserver for measuring items
    let itemObserver: ResizeObserver | null = null;
    let observedIndices = new Set<number>();

    $effect(() => {
        if (!scrollEl || isLegacyApi || !vs) return;

        // ResizeObserver: measures item heights when they render/change.
        itemObserver = new ResizeObserver((entries) => {
            for (const entry of entries) {
                const target = entry.target as HTMLElement;
                const index = parseInt(target.getAttribute('data-virtual-index') ?? '', 10);
                if (isNaN(index)) continue;
                const h = entry.borderBoxSize?.[0]?.blockSize ?? entry.contentRect.height;
                if (h > 0) {
                    vs.measureItem(index, Math.ceil(h));
                }
            }
        });

        // Observe initially rendered items.
        const itemEls = scrollEl.querySelectorAll<HTMLElement>('.virtual-item[data-virtual-index]');
        // Prune stale indices — removed virtual items leave dead entries behind.
        // This bounds observedIndices to only indices whose elements are still in the DOM.
        const aliveIndices = new Set<number>();
        for (const el of itemEls) {
            const index = parseInt(el.getAttribute('data-virtual-index') ?? '', 10);
            if (isNaN(index)) continue;
            aliveIndices.add(index);
        }
        for (const index of observedIndices) {
            if (!aliveIndices.has(index)) {
                observedIndices.delete(index);
            }
        }
        for (const el of itemEls) {
            const index = parseInt(el.getAttribute('data-virtual-index') ?? '', 10);
            if (isNaN(index) || observedIndices.has(index)) continue;
            observedIndices.add(index);
            itemObserver.observe(el);
        }

        // MutationObserver picks up new items from scroll (no reactive cycle).
        const spacer = scrollEl.querySelector('.virtual-spacer');
        let mo: MutationObserver | null = null;
        if (spacer) {
            mo = new MutationObserver((mutations) => {
                for (const mutation of mutations) {
                    // Handle removed nodes first — clean up observed indices
                    for (const node of mutation.removedNodes) {
                        if (node.nodeType !== 1) continue;
                        const el = node as HTMLElement;
                        const removedItems = el.classList?.contains('virtual-item')
                            ? [el]
                            : Array.from(el.querySelectorAll<HTMLElement>('.virtual-item[data-virtual-index]'));
                        for (const item of removedItems) {
                            const index = parseInt(item.getAttribute('data-virtual-index') ?? '', 10);
                            if (!isNaN(index)) observedIndices.delete(index);
                        }
                    }
                    // Handle added nodes — observe new items
                    for (const node of mutation.addedNodes) {
                        if (node.nodeType !== 1) continue;
                        const el = node as HTMLElement;
                        // Direct virtual-item or parent of one
                        const items = el.classList?.contains('virtual-item')
                            ? [el]
                            : Array.from(el.querySelectorAll<HTMLElement>('.virtual-item[data-virtual-index]'));
                        for (const item of items) {
                            const index = parseInt(item.getAttribute('data-virtual-index') ?? '', 10);
                            if (isNaN(index) || observedIndices.has(index)) continue;
                            observedIndices.add(index);
                            itemObserver?.observe(item);
                        }
                    }
                }
            });
            mo.observe(spacer, { childList: true });
        }

        return () => {
            itemObserver?.disconnect();
            itemObserver = null;
            mo?.disconnect();
            mo = null;
            observedIndices.clear();
        };
    });

    /** Scroll to the bottom of the list. */
    function scrollToBottom() {
        if (scrollEl) {
            scrollEl.scrollTop = isLegacyApi ? totalHeight : (vs?.totalHeight ?? 0);
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
        const h = isLegacyApi ? totalHeight : (vs?.totalHeight ?? 0);
        const gap = h - scrollEl.scrollTop - scrollEl.clientHeight;
        return gap < threshold;
    }

    /** Scroll to a specific item index. */
    function scrollToItem(index: number, behavior: ScrollBehavior = 'auto') {
        if (index < 0 || index >= items.length) return;
        if (isLegacyApi) {
            const h = typeof itemHeight === 'number' ? itemHeight : 28;
            scrollToOffset(index * h, behavior);
        } else if (vs) {
            scrollToOffset(vs.getItemOffset(index), behavior);
        }
    }

    // Expose methods and state for parent access
    export { scrollEl as scrollElement, scrollToBottom, scrollToOffset, isNearBottom, scrollToItem };
    export { vs };
</script>

{#if isLegacyApi}
    {#if virtualized}
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
{:else}
    <div
        class="virtual-list"
        bind:this={scrollEl}
        role={role}
        aria-label={ariaLabel}
        tabindex="0"
        onscroll={onscroll}
        onwheel={onwheel}
        ontouchstart={ontouchstart}
        onkeydown={handleKeydown}
    >
        <div class="virtual-spacer" style="height: {vs?.totalHeight ?? 0}px; position: relative;">
            {#each newVisibleItems as { item, index, offset }}
                <div
                    class="virtual-item"
                    style="position: absolute; top: {offset}px; left: 0; right: 0;"
                    data-virtual-index={index}
                >
                    {@render children({ item, index, offset })}
                </div>
            {/each}
        </div>
    </div>
{/if}

<style>
    .virtual-scroll,
    .virtual-list {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
        overflow-y: auto;
        overflow-x: hidden;
        outline: none;
    }
    .virtual-scroll:focus-visible,
    .virtual-list:focus-visible {
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
    .virtual-item {
        padding: 0 12px;
        will-change: transform;
    }
</style>
