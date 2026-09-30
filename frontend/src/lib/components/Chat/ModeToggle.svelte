<script lang="ts">
    import { t } from '$lib/i18n';
    import { modeTip } from '$lib/constants/modes';
    import ModeIcon from '../ModeIcon.svelte';

    /**
     * The operating-mode button, in the two looks it is worn in: `bar` in the chat form's model bar,
     * `panel` in the start panel. Whether a toggle exists at all stays the caller's question — an
     * agent provider has no modes, and the two callers answer it from different state.
     */
    let { mode, onToggle, variant }: {
        mode: string;
        onToggle: () => void;
        variant: 'bar' | 'panel';
    } = $props();
</script>

<button
    type="button"
    class="btn-plan-toggle {variant}"
    class:active={mode === 'plan'}
    class:explore={mode === 'explore'}
    onclick={onToggle}
    title={modeTip(mode)}
    aria-label={t('chat.mode.toggleAria')}
>
    <ModeIcon {mode} />
    <span>{mode === 'plan' ? t('chat.mode.plan') : mode === 'explore' ? t('chat.mode.explore') : t('chat.mode.act')}</span>
</button>

<style>
    .btn-plan-toggle {
        align-items: center;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
    }
    /* Sits at the right edge of the model bar; `all` is what animates a mode change. */
    .btn-plan-toggle.bar {
        display: flex;
        flex-shrink: 0;
        margin-left: auto;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
        transition: all var(--dur-base) ease;
    }
    .btn-plan-toggle.bar:hover {
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .btn-plan-toggle.bar.active {
        background: color-mix(in srgb, var(--color-accent) 22%, transparent);
        border-color: var(--color-accent);
        color: var(--color-btn-text);
    }
    .btn-plan-toggle.bar.explore {
        background: color-mix(in srgb, var(--color-status-yellow) 15%, transparent);
        border-color: var(--color-status-yellow);
        color: var(--color-status-yellow);
    }
    .btn-plan-toggle.panel {
        display: inline-flex;
        gap: 6px;
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: var(--text-sm);
    }
    .btn-plan-toggle.panel:hover {
        opacity: 1;
        background: var(--color-surface-4);
    }
    /* The active and explore states gain a border, so they give back the padding it would steal. */
    .btn-plan-toggle.panel.active {
        padding: 4px 10px;
        background: var(--color-surface-3);
        border: 1px solid var(--color-accent-light);
        color: var(--color-accent-light);
    }
    .btn-plan-toggle.panel.explore {
        padding: 4px 10px;
        border: 1px solid var(--color-status-yellow);
        color: var(--color-status-yellow);
    }
</style>
