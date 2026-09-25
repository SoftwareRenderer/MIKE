<script lang="ts">
    import { t } from '$lib/i18n';
    import { renderDiagram, type DiagramAnswer } from '$lib/utils/diagram';
    import FenceCopyButton from './FenceCopyButton.svelte';

    interface Props {
        /** The fence body: a diagram source, and the code the toggle switches to. */
        text: string;
    }

    let { text }: Props = $props();

    /** `null` while the renderer works, which for the first diagram includes fetching the renderer. */
    let answer = $state<DiagramAnswer | null>(null);
    let showCode = $state(false);

    $effect(() => {
        const source = text;
        answer = null;
        let live = true;
        void renderDiagram(source).then((result) => {
            if (live) answer = result;
        });
        return () => { live = false; };
    });

    /** What the copy control hands out: the picture when the picture is on screen. */
    const diagram = $derived(answer?.ok === true ? answer : null);
    const showingDiagram = $derived(diagram !== null && !showCode);
    const copyValue = $derived(showingDiagram && diagram ? diagram.markup : text);
</script>

<div class="diagram-fence">
    {#if diagram}
        <div class="fence-tabs" role="group">
            <button type="button" class="tab" class:active={!showCode} aria-pressed={!showCode}
                onclick={() => { showCode = false; }}>{t('chat.diagram.viewDiagram')}</button>
            <button type="button" class="tab" class:active={showCode} aria-pressed={showCode}
                onclick={() => { showCode = true; }}>{t('chat.diagram.viewSource')}</button>
            <span class="spacer"></span>
            <FenceCopyButton value={copyValue}
                label={showingDiagram ? t('chat.diagram.copySvg') : t('chat.code.copy')} />
        </div>
    {/if}

    {#if showingDiagram && diagram}
        <img class="diagram" src={diagram.imageSrc} alt={t('chat.diagram.alt')} />
    {:else}
        <pre><code>{text}</code></pre>
        {#if answer === null}
            <p class="note" role="status">{t('chat.diagram.rendering')}</p>
        {:else if !answer.ok}
            <p class="note">{answer.reason === 'too-large' ? t('chat.diagram.tooLarge') : t('chat.diagram.failed')}</p>
        {/if}
    {/if}
</div>

<style>
    /* The source needs no rule here: `ChatMessage.svelte` styles every `pre` in an assistant bubble, and
       sharing that rule is what makes it look like any other code block in the same message. */
    .fence-tabs {
        display: flex;
        align-items: center;
        gap: 0.35rem;
        margin-top: 8px;
    }
    .spacer {
        flex: 1;
    }
    .tab {
        padding: 2px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: 11px;
        cursor: pointer;
    }
    .tab:hover {
        color: var(--color-text-primary);
    }
    .tab.active {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .diagram {
        display: block;
        margin: 8px 0;
        /* The image carries a viewBox and a percentage width, so a wide diagram arrives short rather
           than cut off. */
        max-width: 100%;
        height: auto;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
    }
    .note {
        margin: 0.35rem 0 0;
        color: var(--color-text-secondary);
        font-size: 11px;
    }
</style>
