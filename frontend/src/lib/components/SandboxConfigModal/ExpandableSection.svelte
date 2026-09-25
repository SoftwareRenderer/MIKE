<script lang="ts">
    /**
     * One collapsible section of the Sandbox Configuration form: a ruled row that opens a block
     * under itself. The block's class comes from the caller because that is the class the section's
     * own rules are written under (`ports-content`, `mounts-content`, …).
     */
    import type { Snippet } from 'svelte';
    import { slide } from 'svelte/transition';

    let { label, desc, ariaLabel, expanded = $bindable(false), contentClass, subsection = false, icon, children }: {
        label: string;
        /** The sentence shown only while the section is open. */
        desc: string;
        ariaLabel: string;
        expanded?: boolean;
        /** The class the open block carries; the style that block's section writes lives under it. */
        contentClass: string;
        /** Nested under a switch, so its rows line up under that switch's label. */
        subsection?: boolean;
        icon: Snippet<[]>;
        children?: Snippet<[]>;
    } = $props();
</script>

<div class="config-row-group" class:expanded>
    <button type="button" class="config-row {subsection ? 'config-row-subsection' : 'config-row-expandable'}"
            onclick={() => { expanded = !expanded; }} aria-expanded={expanded} aria-label={ariaLabel}>
        <div class="config-info">
            <div class="config-icon">
                {@render icon()}
            </div>
            <div class="config-text">
                <span class="config-label" class:collapsed={!expanded}>{label}</span>
                {#if expanded}
                    <span class="config-desc">{desc}</span>
                {/if}
            </div>
        </div>
        <svg class="chevron" class:chevron-open={expanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>

    {#if expanded}
        <div class={contentClass} transition:slide={{ duration: 200 }}>
            {@render children?.()}
        </div>
    {/if}
</div>

<style>
    @import '$lib/styles/sandbox-config.css';
</style>
