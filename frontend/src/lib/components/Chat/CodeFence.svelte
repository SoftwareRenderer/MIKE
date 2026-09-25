<script lang="ts">
    import { t } from '$lib/i18n';
    import FenceCopyButton from './FenceCopyButton.svelte';

    interface Props {
        lang: string;
        text: string;
    }

    let { lang, text }: Props = $props();
</script>

<div class="code-fence">
    <pre><code class={lang ? `language-${lang}` : undefined}>{text}</code></pre>
    <FenceCopyButton value={text} label={t('chat.code.copy')} />
</div>

<style>
    .code-fence {
        position: relative;
    }
    /* Where the control sits is the code block's business; what it does is FenceCopyButton's. */
    .code-fence :global(.copy-btn) {
        position: absolute;
        top: 6px;
        right: 10px;
        /* A touch screen has no hover, so the breakpoint rule below shows it there. */
        opacity: 0;
        pointer-events: none;
    }
    .code-fence:hover :global(.copy-btn),
    .code-fence:focus-within :global(.copy-btn) {
        opacity: 1;
        pointer-events: auto;
    }
    @media (max-width: 768px) {
        .code-fence :global(.copy-btn) {
            opacity: 1;
            pointer-events: auto;
        }
    }
</style>
