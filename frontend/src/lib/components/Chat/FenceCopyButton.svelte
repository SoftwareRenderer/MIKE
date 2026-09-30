<script lang="ts">
    import { t } from '$lib/i18n';
    import { copyText } from '$lib/utils/clipboard';

    interface Props {
        /** Whatever is on screen — a fence's source, or the markup of the image drawn from it. */
        value: string;
        /** The tooltip's words, which say what the clipboard will hold. */
        label: string;
    }

    let { value, label }: Props = $props();

    let copied = $state(false);
    let revert: ReturnType<typeof setTimeout> | undefined;

    $effect(() => () => clearTimeout(revert));

    async function handleCopy() {
        // A check mark claims the clipboard holds the text, so a refused write shows nothing.
        if (!(await copyText(value))) return;
        copied = true;
        clearTimeout(revert);
        revert = setTimeout(() => { copied = false; }, 2000);
    }
</script>

<button
    type="button"
    class="copy-btn"
    class:copied
    onclick={handleCopy}
    aria-label={copied ? t('chat.code.copied') : label}
    title={copied ? t('chat.code.copied') : label}
>
    <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
        <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
    </svg>
    <span class="check" aria-hidden="true">✓</span>
</button>

<style>
    .copy-btn {
        /* The check mark below is absolutely positioned, and the button is its containing block: the
           control owns its own layout whatever renders around it. */
        position: relative;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        padding: 0;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: opacity var(--dur-base) ease, color var(--dur-base) ease;
    }
    .copy-btn:hover {
        color: var(--color-text-primary);
    }
    .copy-btn .icon {
        transition: opacity var(--dur-md) ease;
    }
    .copy-btn.copied .icon {
        opacity: 0;
        position: absolute;
    }
    .copy-btn .check {
        position: absolute;
        opacity: 0;
        font-size: var(--text-base);
        font-weight: 700;
        line-height: 1;
        pointer-events: none;
        transition: opacity var(--dur-md) ease;
    }
    .copy-btn.copied .check {
        opacity: 1;
    }
    @media (prefers-reduced-motion: reduce) {
        .copy-btn {
            transition: none;
        }
    }
</style>
