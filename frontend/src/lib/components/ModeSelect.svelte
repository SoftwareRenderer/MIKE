<script lang="ts">
    import { t, type MessageKey } from '$lib/i18n';
    import { useClickOutside } from '$lib/composables/use-click-outside.svelte';
    import ModeIcon from './ModeIcon.svelte';

    let { value, onChange }: { value: string; onChange: (mode: string) => void } = $props();

    // `value` is the operating_mode the server stores — data, never translated. Only
    // `labelKey` is copy, resolved in the markup so a locale change re-labels the control.
    const modes: { value: string; labelKey: MessageKey }[] = [
        { value: 'act', labelKey: 'chat.mode.act' },
        { value: 'plan', labelKey: 'chat.mode.plan' },
        { value: 'explore', labelKey: 'chat.mode.explore' },
    ];

    let open = $state(false);
    let dropdownRef: HTMLDivElement | null = null;
    let selected = $derived(modes.find(m => m.value === value) || modes[0]);

    useClickOutside(() => dropdownRef, () => { open = false; }, { event: 'mousedown' });

    function toggle() {
        open = !open;
    }

    function select(mode: string) {
        onChange(mode);
        open = false;
    }
</script>

<div class="mode-select" bind:this={dropdownRef}>
    <button type="button" class="mode-btn" class:open={open} onclick={toggle} onkeydown={(e) => { if (e.key === 'Escape') open = false; }} aria-label={t('chat.mode.aria')} aria-expanded={open}>
        <span class="mode-icon active" class:act={value === 'act'} class:plan={value === 'plan'} class:explore={value === 'explore'}><ModeIcon mode={value} /></span>
        <span class="mode-label">{t(selected.labelKey)}</span>
        <svg class="chevron" width="10" height="10" viewBox="0 0 10 10" fill="none">
            <path d="M2.5 4L5 6.5L7.5 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>
    {#if open}
        <div class="mode-dropdown" role="listbox" tabindex="-1" onkeydown={(e) => { if (e.key === 'Escape') open = false; }}>
            {#each modes as mode}
                <button
                    type="button"
                    class="mode-option"
                    class:selected={mode.value === value}
                    class:act={mode.value === 'act'}
                    class:plan={mode.value === 'plan'}
                    class:explore={mode.value === 'explore'}
                    role="option"
                    aria-selected={mode.value === value}
                    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); select(mode.value); } }}
                    onclick={() => select(mode.value)}
                >
                    <span class="mode-icon" class:active={mode.value === value} class:act={mode.value === 'act'} class:plan={mode.value === 'plan'} class:explore={mode.value === 'explore'}><ModeIcon mode={mode.value} /></span>
                    <span>{t(mode.labelKey)}</span>
                </button>
            {/each}
        </div>
    {/if}
</div>

<style>
    .mode-select {
        position: relative;
        display: inline-block;
    }
    .mode-btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 5px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        font-family: inherit;
        white-space: nowrap;
        transition: border-color 0.15s;
    }
    .mode-btn:hover {
        border-color: var(--color-accent);
    }
    .mode-btn.open {
        border-color: var(--color-accent);
    }
    .mode-label {
        line-height: 1;
    }
    .chevron {
        margin-left: 2px;
        transition: transform 0.15s;
    }
    .mode-btn.open .chevron {
        transform: rotate(180deg);
    }
    .mode-icon {
        line-height: 1;
        display: flex;
        align-items: center;
        color: var(--color-text-secondary);
    }
    .mode-icon.act.active {
        color: var(--color-text-secondary);
    }
    .mode-icon.plan.active {
        color: var(--color-accent);
    }
    .mode-icon.explore.active {
        color: var(--color-status-yellow);
    }
    .mode-dropdown {
        position: absolute;
        top: calc(100% + 4px);
        right: 0;
        min-width: 140px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 4px 12px rgba(0,0,0,0.15);
        padding: 4px;
        z-index: 50;
    }
    .mode-option {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        padding: 6px 10px;
        border: none;
        border-radius: 3px;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        font-family: inherit;
        text-align: left;
    }
    .mode-option:hover {
        background: var(--color-surface-3);
    }
    .mode-option.act.selected {
        color: var(--color-text-secondary);
    }
    .mode-option.plan.selected {
        color: var(--color-accent);
    }
    .mode-option.explore.selected {
        color: var(--color-status-yellow);
    }
</style>
