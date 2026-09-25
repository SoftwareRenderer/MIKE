<script lang="ts">
    import { markdownToSafeHTML, isDiagramFence } from '$lib/utils/sanitize';
    import { splitMarkdownSegments } from '$lib/utils/markdown-segments';
    import CodeFence from './CodeFence.svelte';
    import DiagramFence from './DiagramFence.svelte';

    interface Props {
        content: string;
    }

    let { content }: Props = $props();

    const segments = $derived(splitMarkdownSegments(content));
</script>

{#each segments as seg}
    {#if seg.kind === 'code'}
        {#if !seg.open && isDiagramFence(seg.lang)}
            <!-- An open fence is a diagram half-written: the source is the honest rendering of it. -->
            <DiagramFence text={seg.text} />
        {:else}
            <CodeFence lang={seg.lang} text={seg.text} />
        {/if}
    {:else}
        {@html markdownToSafeHTML(seg.source)}
    {/if}
{/each}
