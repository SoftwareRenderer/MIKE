<script lang="ts">
    import { t } from '$lib/i18n';
    import { useClickOutside } from '$lib/composables/use-click-outside.svelte';
    import { formatHexColor, parseHexColor } from '$lib/utils/editor-color';
    import type { ColorTarget } from '$lib/utils/editor-color-picker';

    /**
     * The card a clicked color opens: the native picker, the token as text, and Apply — it answers with the
     * whole replacement token, so the editor's only job is to write what it is handed.
     */
    let { target, onApply, onClose }: {
        target: ColorTarget;
        onApply: (token: string) => void;
        onClose: () => void;
    } = $props();

    // The card is one token's and the caller re-creates it (`{#key}`) on another, so this never re-reads
    // the token it opened on.
    // svelte-ignore state_referenced_locally
    let hex = $state(target.color);
    let card = $state<HTMLDivElement | null>(null);
    let field = $state<HTMLInputElement | null>(null);

    const picked = $derived(parseHexColor(hex));

    /** Six digits is all a color input holds; an unparseable field leaves it at its own default. */
    const swatchValue = $derived(picked?.value ?? '');

    /** The card's box; the width rule in the style block has to agree with it. */
    const SIZE = { width: 248, height: 84, gap: 6, edge: 8 };
    const place = $derived.by(() => {
        const left = Math.min(
            Math.max(SIZE.edge, target.left),
            Math.max(SIZE.edge, window.innerWidth - SIZE.width - SIZE.edge),
        );
        const below = target.bottom + SIZE.gap;
        const fits = below + SIZE.height + SIZE.edge <= window.innerHeight;
        return { left, top: fits ? below : Math.max(SIZE.edge, target.top - SIZE.height - SIZE.gap) };
    });

    /** The swatch answers six digits with no alpha, so the token's clothes come from the token. */
    function chose(event: Event) {
        const value = parseHexColor((event.target as HTMLInputElement).value);
        if (value) hex = formatHexColor(hex, value);
    }

    /** The token's case and digit count; the field's color and alpha. */
    function apply() {
        if (!picked) return;
        onApply(formatHexColor(target.color, picked, picked.alpha));
    }

    function onKey(event: KeyboardEvent) {
        // Only the card's own Enter is an Apply: the editor is one tab away and must keep its newline.
        if (event.key === 'Enter' && card?.contains(event.target as Node)) {
            event.preventDefault();
            apply();
        }
        if (event.key === 'Escape') {
            event.preventDefault();
            onClose();
        }
    }

    $effect(() => {
        field?.select();
    });

    useClickOutside(() => card, () => onClose(), { event: 'pointerdown' });

    $effect(() => {
        window.addEventListener('keydown', onKey, true);
        return () => window.removeEventListener('keydown', onKey, true);
    });
</script>

<div
    class="color-card"
    role="dialog"
    aria-label={t('editor.color.title')}
    style={`left: ${place.left}px; top: ${place.top}px`}
    bind:this={card}
>
    <div class="pick">
        <!-- An empty value leaves the input on its own default: no color literal belongs in markup. -->
        <input
            class="swatch"
            type="color"
            value={swatchValue}
            aria-label={t('editor.color.swatch')}
            oninput={chose}
        />
        <input
            class="token"
            type="text"
            bind:this={field}
            bind:value={hex}
            spellcheck="false"
            autocomplete="off"
            aria-label={t('editor.color.hex')}
            aria-invalid={picked ? undefined : 'true'}
        />
    </div>
    <div class="actions">
        <button type="button" class="btn btn-primary btn-small" disabled={!picked} onclick={apply}>
            {t('editor.color.apply')}
        </button>
        <button type="button" class="btn btn-ghost btn-small" onclick={onClose}>{t('editor.color.cancel')}</button>
    </div>
</div>

<style>
    .color-card {
        position: fixed;
        z-index: var(--z-overlay);
        width: 248px;
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding: 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-md);
        box-shadow: var(--shadow-modal);
    }

    .pick {
        display: flex;
        align-items: center;
        gap: 6px;
    }

    .swatch {
        flex: none;
        width: 36px;
        height: 30px;
        padding: 0;
        background: none;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
    }

    .token {
        flex: 1;
        min-width: 0;
        padding: 5px 6px;
        font-family: var(--font-mono);
        font-size: var(--text-sm);
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
    }

    .token[aria-invalid='true'] {
        border-color: var(--color-danger);
    }

    .actions {
        display: flex;
        justify-content: flex-end;
        gap: 6px;
    }
</style>
