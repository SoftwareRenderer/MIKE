<script lang="ts">
    import { markdownToSafeHTML } from '$lib/utils/sanitize';
    import { splitMarkdownSegments } from '$lib/utils/markdown-segments';
    import CodeFence from './CodeFence.svelte';

    interface Props {
        content: string;
    }

    let { content }: Props = $props();

    const segments = $derived(splitMarkdownSegments(content));
</script>

{#each segments as seg}
    {#if seg.kind === 'code'}
        <CodeFence lang={seg.lang} text={seg.text} />
    {:else}
        {@html markdownToSafeHTML(seg.source)}
    {/if}
{/each}
