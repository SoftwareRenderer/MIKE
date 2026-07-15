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
        for (let i = vs.startIdx; i < vs.endIdx; i++) {
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

    $effect(() => {
        if (!scrollEl || isLegacyApi || !vs) return;

        if (itemObserver) itemObserver.disconnect();

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

        const itemEls = scrollEl.querySelectorAll<HTMLElement>('.virtual-item[data-virtual-index]');
        for (const el of itemEls) {
            itemObserver.observe(el);
        }

        return () => {
            itemObserver?.disconnect();
            itemObserver = null;
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
            onscroll={onscroll}
            onwheel={onwheel}
            ontouchstart={ontouchstart}
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
            onscroll={onscroll}
            onwheel={onwheel}
            ontouchstart={ontouchstart}
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
        onscroll={onscroll}
        onwheel={onwheel}
        ontouchstart={ontouchstart}
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
