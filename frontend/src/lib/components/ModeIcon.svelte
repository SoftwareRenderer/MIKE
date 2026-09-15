<script lang="ts">
    /**
     * The operating-mode shapes have one home, so a mode draws the same icon in the chat toggle
     * (`Chat/ModeToggle`) and in the dropdown (`ModeSelect`). Real elements rather than an SVG string,
     * so a shape is never built by a component and then parsed back.
     */
    let { mode }: { mode: string } = $props();
</script>

<svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
    {#if mode === 'plan'}
        <path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
    {:else if mode === 'explore'}
        <path d="M4 6a4 4 0 1 1 8 0c0 2.5-2 3.5-2 3.5v1h-4v-1s-2-1-2-3.5Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
        <path d="M6 10h4v1.5H6Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
        <path d="M6.5 12.5h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
    {:else}
        <!-- A mode the app does not know draws the act shape. -->
        <path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
    {/if}
</svg>
