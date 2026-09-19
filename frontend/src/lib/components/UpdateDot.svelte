<script lang="ts">
    import { t } from '$lib/i18n';
    import { updateCheck, type UpdateDot } from '$lib/services/update-check.svelte';

    /**
     * The one announcement the board makes about releases: a dot on the Settings control, green when a
     * newer release is known, filled when one is downloaded and waiting to be installed, and outlined
     * while the opt-in answer is owed. Everything behind it is said inside Settings → About, which is
     * what the click opens. The host must be positioned.
     * A real element with its own label: the reason for the dot has to reach a person who cannot see color.
     */
    const kind = $derived<UpdateDot>(updateCheck.dot);
    const label = $derived(
        kind === 'ready' ? t('updates.dot.ready')
            : kind === 'update' ? t('updates.dot.update')
                : t('updates.dot.question')
    );
</script>

{#if kind !== 'none'}
    <span
        class="update-dot"
        class:update={kind === 'update'}
        class:ready={kind === 'ready'}
        class:question={kind === 'question'}
        data-update-dot={kind}
        role="img"
        aria-label={label}
        title={label}
    ></span>
{/if}

<style>
    .update-dot {
        position: absolute;
        top: 4px;
        right: 4px;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        pointer-events: none;
    }

    .update-dot.update {
        background: var(--color-status-green);
    }

    /* Downloaded and verified: the install click is what is owed, not the download. */
    .update-dot.ready {
        background: var(--color-status-green);
        box-shadow: 0 0 0 2px var(--color-surface-0);
    }

    /* Nothing to apply yet — this is a question, not news. */
    .update-dot.question {
        background: transparent;
        border: 1.5px solid var(--color-accent);
    }
</style>
