<!--
    The one "nothing here" panel: a centred column of glyph, headline, hint and optional
    action, sized and coloured from the shared tokens so two empty panes read as one app.
    It renders only what it is given — a headline alone is a complete empty state.

    `title` and `hint` are already-translated text: the caller owns the phrase, this
    component never calls `t`. `icon` is the same value `<Icon src>` takes — an
    `*.svg?raw` import — passed through unchanged, so the glyph is drawn in `currentColor`
    and carries no colour of its own.

    No `role="status"`, no live region: an empty state describes a pane rather than a change,
    and a region that mounts when a list empties would be read out as news.

    The component owns how an empty state looks, so a site never restyles it from outside. A
    site that needs a LAYOUT difference — how much of its pane the column takes — passes `class`
    with an app.css hook (`pane-fill`, `parent-height`): a parent's scoped stylesheet cannot
    reach a child component's root element, which is why that geometry cannot live locally.
-->
<script lang="ts">
    import type { Snippet } from 'svelte';
    import Icon from './Icon.svelte';

    interface Props {
        /** Already-translated headline. */
        title: string;
        /** Already-translated second line, quieter than the headline. */
        hint?: string;
        /** `<svg>` markup from `$lib/assets/icons/*.svg?raw`. */
        icon?: string;
        /** Tighter vertical rhythm, for a narrow side panel. */
        compact?: boolean;
        /** Optional call to action, under the text. */
        action?: Snippet<[]>;
        /** A layout hook the site owns — see the header comment. */
        class?: string;
    }

    let { title, hint, icon, compact = false, action, class: className }: Props = $props();

    // The glyph scales with the rhythm rather than with the pane: a wide pane gets a wide
    // column of text, not a bigger icon.
    let glyph = $derived(compact ? 16 : 24);
</script>

<div class="empty-state {className ?? ''}" class:compact>
    {#if icon}
        <Icon src={icon} width={glyph} height={glyph} />
    {/if}
    <p class="title">{title}</p>
    {#if hint}
        <p class="hint">{hint}</p>
    {/if}
    {#if action}
        <div class="action">{@render action()}</div>
    {/if}
</div>

<style>
    .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        /* `margin` keeps the column centred in a block pane and in a flex one alike; `max-width`
           stops a hint from running the width of a maximised diff panel. */
        margin: 0 auto;
        max-width: 320px;
        padding: 32px 16px;
        text-align: center;
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
    }

    .empty-state.compact {
        gap: 4px;
        padding: 12px 8px;
    }

    .title {
        color: var(--color-text-secondary);
        font-weight: 500;
    }
</style>
