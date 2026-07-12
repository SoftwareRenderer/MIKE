<script lang="ts">
    /**
     * Lightweight virtual scroller for fixed-height lists.
     *
     * Usage:
     *   <VirtualList {items} itemHeight={28} minThreshold={50}>
     *     {#snippet children({ item, index })}
     *       <div>{item.name}</div>
     *     {/snippet}
     *   </VirtualList>
     */
    import { onMount, onDestroy, type Snippet } from 'svelte';

    let { items, itemHeight = 28, minThreshold = 50, buffer = 5, role, 'aria-label': ariaLabel, scrollTop: scrollTopProp, children }: {
        items: any[];
        itemHeight?: number;
        minThreshold?: number;
        buffer?: number;
        role?: string;
        'aria-label'?: string;
        scrollTop?: number; // external scroll position
        children?: Snippet<[{
            item: any;
            index: number;
        }]>;
    } = $props();

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let scrollEl: HTMLDivElement | null = null;
    let internalScrollTop = $state(0);
    let containerHeight = $state(0);

    let scrollTop = $derived(scrollTopProp ?? internalScrollTop);

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

    onMount(() => {
        if (scrollEl) {
            containerHeight = scrollEl.clientHeight;
            scrollEl.addEventListener('scroll', handleScroll, { passive: true });
            window.addEventListener('resize', handleResize, { passive: true });
        }
    });

    onDestroy(() => {
        scrollEl?.removeEventListener('scroll', handleScroll);
        window.removeEventListener('resize', handleResize);
    });
</script>

{#if virtualized}
    <div class="virtual-scroll" bind:this={scrollEl} role={role} aria-label={ariaLabel}>
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
    <div class="virtual-scroll" bind:this={scrollEl} role={role} aria-label={ariaLabel}>
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
