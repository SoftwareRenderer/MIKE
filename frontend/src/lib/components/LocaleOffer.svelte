<script lang="ts">
    import { fade } from 'svelte/transition';
    import { localeOffer } from '$lib/services/locale-offer.svelte';
    import { t } from '$lib/i18n/runtime.svelte';

    /**
     * The first-run offer: "your browser speaks another language, here is the
     * pack for it" — with one click to install it and one to stop being asked.
     *
     * Chrome, not a flow. It floats in the corner above the board and never
     * takes focus, never traps the keyboard and never blocks a click anywhere
     * else, because the app is usable in the language it is already in. It is
     * shown only when no pack is installed at all, so its own strings are
     * necessarily English (`i18n.offer.*` in `en.ts`) — and the only language
     * name it can print is the pack's own, from the registry index.
     *
     * The decision, the install and the dismissal record all live in
     * `$lib/services/locale-offer.svelte.ts`; this renders state and fires two
     * clicks.
     */
    let state = $derived(localeOffer.state);
    let language = $derived(state.displayName);
    let installing = $derived(state.phase === 'installing');
    let visible = $derived(state.phase === 'offered' || installing);
</script>

{#if visible}
    <div
        class="locale-offer"
        transition:fade={{ duration: 150 }}
        role="status"
        aria-live="polite"
    >
        <p class="locale-offer-title">{t('i18n.offer.question', { language })}</p>
        <p class="locale-offer-detail">{t('i18n.offer.detail', { language })}</p>

        {#if installing}
            <!-- The install's own progress, from the operation's SSE stream: the
                 server says which step it is on, so a git clone that takes a
                 moment is visibly working rather than hung. -->
            <p class="locale-offer-progress locale-offer-step-state">
                <span class="locale-offer-spin" aria-hidden="true"></span>
                {t('i18n.offer.installing', { language })}
                {#if state.progress > 0}<span class="locale-offer-pct">{Math.round(state.progress)}%</span>{/if}
            </p>
            <p class="locale-offer-step" aria-hidden="true">{state.message}</p>
        {:else}
            <div class="locale-offer-actions">
                <button
                    type="button"
                    class="btn btn-primary btn-small locale-offer-install"
                    onclick={() => void localeOffer.install()}
                >
                    {t('i18n.offer.install', { language })}
                </button>
                <button
                    type="button"
                    class="btn btn-small locale-offer-dismiss"
                    onclick={() => void localeOffer.dismiss()}
                >
                    {t('i18n.offer.never')}
                </button>
            </div>
        {/if}
    </div>
{/if}

<style>
    /* Bottom-right: the toast and the service-worker bar own bottom-centre, and
       this has to survive both appearing without either losing its text. Below
       the modals' z-index — a dialog is more important than an offer. */
    .locale-offer {
        position: fixed;
        right: 16px;
        bottom: 16px;
        z-index: 900;
        width: 340px;
        max-width: calc(100vw - 32px);
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 12px 14px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-lg);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35);
    }

    .locale-offer-title {
        margin: 0;
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .locale-offer-detail {
        margin: 0;
        font-size: 0.75rem;
        line-height: 1.45;
        color: var(--color-text-secondary);
    }

    .locale-offer-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 2px;
    }

    .locale-offer-progress {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        margin: 2px 0 0 0;
        font-size: 0.75rem;
        color: var(--color-text-primary);
    }

    .locale-offer-pct {
        color: var(--color-text-secondary);
        font-variant-numeric: tabular-nums;
    }

    .locale-offer-step {
        margin: 0;
        font-size: 0.6875rem;
        color: var(--color-text-tertiary, var(--color-text-secondary));
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .locale-offer-spin {
        width: 10px;
        height: 10px;
        border: 2px solid var(--color-border);
        border-top-color: var(--color-text-primary);
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
    }
</style>
