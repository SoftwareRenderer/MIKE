<script lang="ts" module>
    /** One answer the menu offers. */
    export interface FileMenuItem {
        label: string;
        icon?: 'open' | 'window' | 'cut' | 'copy' | 'paste' | 'rename' | 'delete' | 'stage' | 'unstage' | 'discard';
        disabled?: boolean;
        /** Draws as a dangerous action; the caller has already asked for confirmation. */
        destructive?: boolean;
        run: () => void;
    }
</script>

<script lang="ts">
    // The caller decides what the menu offers; this component only places and dismisses the box.
    // The box is fixed to the viewport and clamped after measuring, so it stays on screen.
    import { onMount } from 'svelte';
    import { useClickOutside } from '$lib/composables/use-click-outside.svelte';
    import Icon from './Icon.svelte';
    import stageIcon from '$lib/assets/icons/stage.svg?raw';
    import unstageIcon from '$lib/assets/icons/unstage.svg?raw';
    import discardIcon from '$lib/assets/icons/discard.svg?raw';

    let {
        x,
        y,
        items,
        ariaLabel,
        onClose,
    }: {
        x: number;
        y: number;
        items: FileMenuItem[];
        ariaLabel: string;
        onClose: () => void;
    } = $props();

    const EDGE_MARGIN = 8;

    let menuEl = $state<HTMLDivElement | null>(null);
    // Hidden until measured, so it doesn't flash at the pointer before being clamped.
    let pos = $state({ left: 0, top: 0, ready: false });
    // Focus to give back if the menu is dismissed without choosing.
    let trigger: HTMLElement | null = null;

    $effect(() => {
        const el = menuEl;
        if (!el) return;
        // Reading x and y here means a second right-click moves the open menu.
        const rect = el.getBoundingClientRect();
        pos = {
            left: Math.max(EDGE_MARGIN, Math.min(x, window.innerWidth - rect.width - EDGE_MARGIN)),
            top: Math.max(EDGE_MARGIN, Math.min(y, window.innerHeight - rect.height - EDGE_MARGIN)),
            ready: true,
        };
    });

    onMount(() => {
        trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        queueMicrotask(() => focusItem(0));
    });

    // Capture phase: a press on another control must close the menu before that control reacts.
    useClickOutside(() => menuEl, () => close(), { event: 'pointerdown', capture: true });

    $effect(() => {
        const onKey = (e: KeyboardEvent) => {
            if (e.key !== 'Escape') return;
            // Stop here, or the explorer behind the menu closes too.
            e.stopPropagation();
            e.preventDefault();
            close();
        };
        // The box is fixed to the viewport, so scrolling would leave it over the wrong rows.
        const dismiss = () => close();
        window.addEventListener('keydown', onKey, true);
        window.addEventListener('scroll', dismiss, true);
        window.addEventListener('resize', dismiss);
        return () => {
            window.removeEventListener('keydown', onKey, true);
            window.removeEventListener('scroll', dismiss, true);
            window.removeEventListener('resize', dismiss);
        };
    });

    function enabledItems(): HTMLButtonElement[] {
        return [...(menuEl?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? [])];
    }

    function focusItem(index: number): void {
        const buttons = enabledItems();
        if (buttons.length === 0) return;
        buttons[(index + buttons.length) % buttons.length]?.focus();
    }

    function handleKeydown(e: KeyboardEvent): void {
        const buttons = enabledItems();
        if (buttons.length === 0) return;
        const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
        if (e.key === 'ArrowDown') { e.preventDefault(); focusItem(at + 1); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); focusItem(at - 1); }
        else if (e.key === 'Home') { e.preventDefault(); focusItem(0); }
        else if (e.key === 'End') { e.preventDefault(); focusItem(buttons.length - 1); }
    }

    function close(): void {
        onClose();
        // Give focus back to the row the menu opened from. A chosen item moves focus itself.
        if (trigger && document.contains(trigger)) trigger.focus();
    }

    function runItem(item: FileMenuItem): void {
        if (item.disabled) return;
        const chosen = item.run;
        onClose();
        chosen();
    }
</script>

<div
    class="file-context-menu"
    role="menu"
    tabindex="-1"
    aria-label={ariaLabel}
    bind:this={menuEl}
    class:measuring={!pos.ready}
    style="left: {pos.left}px; top: {pos.top}px"
    onkeydown={handleKeydown}
    oncontextmenu={(e) => e.preventDefault()}
>
    {#each items as item}
        <button
            type="button"
            role="menuitem"
            class="menu-item"
            class:destructive={item.destructive}
            disabled={item.disabled}
            onclick={() => runItem(item)}
        >
            <span class="menu-icon" aria-hidden="true">
                {#if item.icon === 'cut'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/>
                        <line x1="20" y1="4" x2="8.12" y2="15.88"/><line x1="14.47" y1="14.48" x2="20" y2="20"/><line x1="8.12" y1="8.12" x2="12" y2="12"/>
                    </svg>
                {:else if item.icon === 'copy'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>
                    </svg>
                {:else if item.icon === 'paste'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <rect width="8" height="4" x="8" y="2" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>
                    </svg>
                {:else if item.icon === 'rename'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/>
                    </svg>
                {:else if item.icon === 'delete'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M3 6h18"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
                        <path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/>
                    </svg>
                {:else if item.icon === 'open'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>
                    </svg>
                {:else if item.icon === 'window'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                        <rect x="2" y="4" width="20" height="16" rx="2"/><path d="M2 9h20"/><path d="M5.5 6.5h2"/>
                    </svg>
                {:else if item.icon === 'stage'}
                    <Icon src={stageIcon} width={14} height={14} />
                {:else if item.icon === 'unstage'}
                    <Icon src={unstageIcon} width={14} height={14} />
                {:else if item.icon === 'discard'}
                    <Icon src={discardIcon} width={14} height={14} />
                {/if}
            </span>
            <span class="menu-label">{item.label}</span>
        </button>
    {/each}
</div>

<style>
    .file-context-menu.measuring {
        visibility: hidden;
    }
    .file-context-menu {
        position: fixed;
        z-index: var(--z-overlay);
        min-width: 190px;
        display: flex;
        flex-direction: column;
        padding: 4px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: var(--shadow-popover);
    }
    .menu-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 10px;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-primary);
        font-size: var(--text-sm);
        text-align: left;
        cursor: pointer;
        white-space: nowrap;
    }
    .menu-item:hover:not(:disabled),
    .menu-item:focus-visible {
        background: var(--color-surface-3);
        outline: none;
    }
    .menu-item:disabled {
        color: var(--color-text-tertiary);
    }
    .menu-item.destructive {
        color: var(--color-status-red);
    }
    .menu-icon {
        display: flex;
        align-items: center;
        width: 14px;
        flex-shrink: 0;
    }
    .menu-label {
        overflow: hidden;
        text-overflow: ellipsis;
    }
</style>
