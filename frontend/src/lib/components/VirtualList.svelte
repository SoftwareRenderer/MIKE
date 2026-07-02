<script lang="ts">
    /**
     * Lightweight virtual scroller for fixed-height lists.
     *
     * Usage:
     *   <VirtualList {items} itemHeight={28} minThreshold={50} let:item let:index>
     *     <div>{item.name}</div>
     *   </VirtualList>
     */
    import { onMount, onDestroy } from 'svelte';

    let { items, itemHeight = 28, minThreshold = 50, buffer = 5, role, 'aria-label': ariaLabel }: {
        items: any[];
        itemHeight?: number;
        minThreshold?: number;
        buffer?: number;
        role?: string;
        'aria-label'?: string;
    } = $props();

    let scrollEl: HTMLDivElement | null = null;
    let scrollTop = $state(0);
    let containerHeight = $state(0);

    let virtualized = $derived(items.length >= minThreshold);
    let totalHeight = $derived(items.length * itemHeight);
    let startIndex = $derived(Math.max(0, Math.floor(scrollTop / itemHeight) - buffer));
    let endIndex = $derived(Math.min(items.length, Math.ceil((scrollTop + containerHeight) / itemHeight) + buffer));
    let visibleOffset = $derived(startIndex * itemHeight);

    function handleScroll() {
        if (scrollEl) scrollTop = scrollEl.scrollTop;
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
                        <slot {item} index={idx} />
                    {/if}
                {/each}
            </div>
        </div>
    </div>
{:else}
    <div class="virtual-scroll" bind:this={scrollEl} role={role} aria-label={ariaLabel}>
        {#each items as item, index}
            <slot {item} {index} />
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
