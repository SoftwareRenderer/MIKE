<script lang="ts">
    import { t } from '$lib/i18n';
    import { copyText } from '$lib/utils/clipboard';

    interface Props {
        lang: string;
        text: string;
    }

    let { lang, text }: Props = $props();

    let copied = $state(false);
    let revert: ReturnType<typeof setTimeout> | undefined;

    $effect(() => () => clearTimeout(revert));

    async function handleCopy() {
        // A check mark claims the clipboard holds the text, so a refused write shows nothing.
        if (!(await copyText(text))) return;
        copied = true;
        clearTimeout(revert);
        revert = setTimeout(() => { copied = false; }, 2000);
    }
</script>

<div class="code-fence">
    <pre><code class={lang ? `language-${lang}` : undefined}>{text}</code></pre>
    <button
        type="button"
        class="copy-btn"
        class:copied
        onclick={handleCopy}
        aria-label={copied ? t('chat.code.copied') : t('chat.code.copy')}
        title={copied ? t('chat.code.copied') : t('chat.code.copy')}
    >
        <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
        </svg>
        <span class="check" aria-hidden="true">✓</span>
    </button>
</div>

<style>
    .code-fence {
        position: relative;
    }
    .copy-btn {
        position: absolute;
        top: 6px;
        right: 10px;
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
        /* Hover reveals it below; a touch screen has no hover, so the breakpoint rule shows it there. */
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.15s ease, color 0.15s ease;
    }
    .code-fence:hover .copy-btn,
    .code-fence:focus-within .copy-btn {
        opacity: 1;
        pointer-events: auto;
    }
    .copy-btn:hover {
        color: var(--color-text-primary);
    }
    @media (max-width: 768px) {
        .copy-btn {
            opacity: 1;
            pointer-events: auto;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .copy-btn {
            transition: none;
        }
    }
    .copy-btn .icon {
        transition: opacity 0.2s ease;
    }
    .copy-btn.copied .icon {
        opacity: 0;
        position: absolute;
    }
    .copy-btn .check {
        position: absolute;
        opacity: 0;
        font-size: 14px;
        font-weight: 700;
        line-height: 1;
        pointer-events: none;
        transition: opacity 0.2s ease;
    }
    .copy-btn.copied .check {
        opacity: 1;
    }
</style>
