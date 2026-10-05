<script lang="ts">
    import { singleSvgMarkup, safeCSSValue } from '$lib/utils/sanitize';

    let { src, color = 'currentColor', width = 16, height = 16, title, class: className }: { src: string; color?: string; width?: number; height?: number; title?: string; class?: string } = $props();

    // These three go into a `style` attribute, where a `;` starts a declaration of its own:
    // `safeCSSValue` refuses such a colour, and a size that is not a number falls back to 16.
    let safeColor = $derived(safeCSSValue(color, 'currentColor'));
    let safeWidth = $derived(Number(width) || 16);
    let safeHeight = $derived(Number(height) || 16);

    // `src` is rendered as markup, so only the shape this component is for is
    // accepted: a lone, sanitized `<svg>` root. A call site passes an
    // `$lib/assets/icons/*.svg?raw` import or a prop documented as holding one,
    // and anything else renders nothing rather than whatever it was.
    let safeSrc = $derived(singleSvgMarkup(src));

    // A dropped icon must not be silent for the developer who asked for one. In an
    // effect rather than at init, so it re-checks when `src` changes instead of
    // reading the first value forever (which is what Svelte is warning about).
    $effect(() => {
        if (import.meta.env.DEV && src && !safeSrc) {
            console.warn('[Icon] src is not a single <svg> element; rendering nothing:', src.slice(0, 80));
        }
    });
</script>

<span class="icon {className}" aria-hidden="true" style="--icon-color: {safeColor}; --icon-width: {safeWidth}px; --icon-height: {safeHeight}px" title={title}>{@html safeSrc}</span>

<style>
    :global(.icon svg) {
        width: var(--icon-width);
        height: var(--icon-height);
        color: var(--icon-color);
        display: block;
    }
</style>
