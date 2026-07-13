<script lang="ts">
    let { src, color = 'currentColor', width = 16, height = 16, title, class: className }: { src: string; color?: string; width?: number; height?: number; title?: string; class?: string } = $props();
</script>

<span class="icon {className}" aria-hidden="true" style="--icon-color: {color}; --icon-width: {width}px; --icon-height: {height}px" title={title}>{@html src}</span>

<style>
    :global(.icon svg) {
        width: var(--icon-width);
        height: var(--icon-height);
        color: var(--icon-color);
        display: block;
    }
</style>
