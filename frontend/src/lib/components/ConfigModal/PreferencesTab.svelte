<script lang="ts">
    import { DEFAULT_SHORTCUTS, normalizeKey, normalizeClick, formatShortcut, defaultCreateTaskSubmitKey, defaultStartTaskKey, defaultMultiSelectClick } from '$lib/utils/keyboard';
    import { notificationPermission, requestNotificationPermission, type PermissionAnswer } from '$lib/utils/notifications';
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';

    let {
        createTaskKey = $bindable<string | undefined>(),
        createTaskSubmitKey = $bindable<string | undefined>(),
        openTerminalKey = $bindable<string | undefined>(),
        startTaskKey = $bindable<string | undefined>(),
        toggleFullscreenKey = $bindable<string | undefined>(),
        multiSelectClickKey = $bindable<string | undefined>(),
        hideHints = $bindable(false),
        hideModelBadge = $bindable(false),
        sortOldestFirst = $bindable(false),
        autoMaximizeChat = $bindable(true),
        notifyOnReview = $bindable(false),
        onDirty,
    }: {
        createTaskKey?: string | undefined;
        createTaskSubmitKey?: string | undefined;
        openTerminalKey?: string | undefined;
        startTaskKey?: string | undefined;
        toggleFullscreenKey?: string | undefined;
        multiSelectClickKey?: string | undefined;
        hideHints?: boolean;
        hideModelBadge?: boolean;
        sortOldestFirst?: boolean;
        autoMaximizeChat?: boolean;
        notifyOnReview?: boolean;
        onDirty?: () => void;
    } = $props();

    // One row per shortcut; the submit and multi-select defaults are platform-aware
    // (Cmd on Mac, Ctrl elsewhere). Only `name`/`desc` are copy — `id` is the API field name
    // and `def` the binding the server stores, so neither is ever translated.
    const rows = $derived([
        {
            id: 'create_task' as const,
            name: t('settings.preferences.shortcut.createTask'),
            desc: t('settings.preferences.shortcut.createTaskDesc'),
            value: createTaskKey,
            def: DEFAULT_SHORTCUTS.create_task,
            click: false,
        },
        {
            id: 'create_task_submit' as const,
            name: t('settings.preferences.shortcut.createTaskSubmit'),
            desc: t('settings.preferences.shortcut.createTaskSubmitDesc'),
            value: createTaskSubmitKey,
            def: defaultCreateTaskSubmitKey(),
            click: false,
        },
        {
            id: 'open_terminal' as const,
            name: t('settings.preferences.shortcut.openTerminal'),
            desc: t('settings.preferences.shortcut.openTerminalDesc'),
            value: openTerminalKey,
            def: DEFAULT_SHORTCUTS.open_terminal,
            click: false,
        },
        {
            id: 'start_task' as const,
            name: t('settings.preferences.shortcut.startTask'),
            desc: t('settings.preferences.shortcut.startTaskDesc'),
            value: startTaskKey,
            def: defaultStartTaskKey(),
            click: false,
        },
        {
            id: 'toggle_fullscreen' as const,
            name: t('settings.preferences.shortcut.toggleFullscreen'),
            desc: t('settings.preferences.shortcut.toggleFullscreenDesc'),
            value: toggleFullscreenKey,
            def: DEFAULT_SHORTCUTS.toggle_fullscreen,
            click: false,
        },
        {
            id: 'multi_select_click' as const,
            name: t('settings.preferences.shortcut.multiSelect'),
            desc: t('settings.preferences.shortcut.multiSelectDesc'),
            value: multiSelectClickKey,
            def: defaultMultiSelectClick(),
            click: true,
        },
    ]);

    let capturingId = $state<string | null>(null);

    /** The row currently capturing a binding — tells a mouse row from a keyboard row. */
    const capturingRow = $derived(rows.find(r => r.id === capturingId));
    let invalidFlash = $state(false);
    /**
     * Which sentence the flash shows. The hint outlives the capture that caused it, so it cannot
     * be derived from `capturingRow`, which is `undefined` the moment capture ends.
     */
    let invalidKind = $state<'key' | 'click'>('key');
    let invalidTimer: ReturnType<typeof setTimeout> | undefined;

    function assign(id: string, value: string | undefined) {
        if (id === 'create_task') createTaskKey = value;
        else if (id === 'create_task_submit') createTaskSubmitKey = value;
        else if (id === 'open_terminal') openTerminalKey = value;
        else if (id === 'start_task') startTaskKey = value;
        else if (id === 'toggle_fullscreen') toggleFullscreenKey = value;
        else if (id === 'multi_select_click') multiSelectClickKey = value;
        onDirty?.();
    }

    function startCapture(id: string) {
        // Click again to cancel.
        capturingId = capturingId === id ? null : id;
        clearFlash();
    }

    /**
     * Hide the rejection hint and cancel its expiry together — the flag is only ever reset by that
     * timer, so clearing one half leaves the hint on screen for good.
     */
    function clearFlash() {
        if (invalidTimer) clearTimeout(invalidTimer);
        invalidTimer = undefined;
        invalidFlash = false;
    }

    function flashInvalid(kind: 'key' | 'click') {
        if (invalidTimer) clearTimeout(invalidTimer);
        invalidKind = kind;
        invalidFlash = true;
        invalidTimer = setTimeout(() => {
            invalidFlash = false;
            invalidTimer = undefined;
        }, 1500);
    }

    // The flash runs on its own clock and survives the capture that caused it, so only unmount cancels it.
    $effect(() => () => clearTimeout(invalidTimer));

    $effect(() => {
        const id = capturingId;
        if (!id) return;
        // This row binds a mouse combo, so it also captures mousedown and refuses keys.
        const clickRow = capturingRow?.click === true;
        const handleKeydown = (e: KeyboardEvent) => {
            e.preventDefault();
            e.stopPropagation();
            if (e.key === 'Escape') {
                capturingId = null;
                return;
            }
            if (clickRow) {
                flashInvalid('click');
                return;
            }
            const normalized = normalizeKey(e);
            if (!normalized) {
                flashInvalid('key');
                return;
            }
            assign(id, normalized);
            clearFlash();
            capturingId = null;
        };
        const handleMousedown = (e: MouseEvent) => {
            // The row's own controls must stay usable while capturing: clicking the chip
            // again cancels, Reset/Clear still work.
            if ((e.target as HTMLElement | null)?.closest?.('.key-chip, .shortcut-action')) return;
            e.preventDefault();
            e.stopPropagation();
            const normalized = normalizeClick(e);
            if (!normalized) {
                flashInvalid('click');
                return;
            }
            assign(id, normalized);
            clearFlash();
            capturingId = null;
        };
        window.addEventListener('keydown', handleKeydown, true);
        if (clickRow) window.addEventListener('mousedown', handleMousedown, true);
        return () => {
            window.removeEventListener('keydown', handleKeydown, true);
            window.removeEventListener('mousedown', handleMousedown, true);
        };
    });

    let permission = $state<PermissionAnswer>(notificationPermission());

    // The enable click is the only gesture a browser accepts a permission prompt from.
    async function onNotifyChange() {
        onDirty?.();
        if (notifyOnReview) permission = await requestNotificationPermission();
    }
</script>

<section class="settings-section">
    <div class="section-header">
        <h3 class="settings-section-title">{t('settings.preferences.keyboard')}</h3>
    </div>

    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">{t('settings.preferences.hideHints')}</span>
            <span id="hide-hints-desc" class="shortcut-desc">{t('settings.preferences.hideHintsDesc')}</span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={hideHints} ariaLabel={t('settings.preferences.hideHints')} ariaDescribedby="hide-hints-desc" onChange={() => onDirty?.()} />
        </div>
    </div>

    {#each rows as row (row.id)}
        <div class="shortcut-row" data-shortcut={row.id}>
            <div class="shortcut-info">
                <span class="shortcut-name">{row.name}</span>
                <span class="shortcut-desc">{row.desc}</span>
            </div>
            <div class="shortcut-controls">
                {#if row.value !== undefined && row.value !== row.def}
                    <button type="button" class="shortcut-action" onclick={() => assign(row.id, undefined)} aria-label={t('settings.preferences.resetToDefault')} title={t('settings.preferences.resetToDefaultWith', { shortcut: formatShortcut(row.def) })}>{t('settings.preferences.reset')}</button>
                {/if}
                {#if row.value ?? row.def}
                    <button type="button" class="shortcut-action" onclick={() => assign(row.id, '')} aria-label={t('settings.preferences.clearShortcut')} title={t('settings.preferences.disableShortcut')}>{t('settings.preferences.clear')}</button>
                {/if}
                <button
                    type="button"
                    class="key-chip"
                    class:capturing={capturingId === row.id}
                    class:invalid={invalidFlash}
                    onclick={() => startCapture(row.id)}
                    aria-label={t('settings.preferences.shortcutAria', {
                        name: row.name,
                        binding: capturingId === row.id
                            ? t('settings.preferences.capturing')
                            : row.value === ''
                                ? t('settings.preferences.disabled')
                                : formatShortcut(row.value ?? row.def),
                    })}
                    title={capturingId === row.id
                        ? (row.click ? t('settings.preferences.clickHint') : t('settings.preferences.keyHint'))
                        : t('settings.preferences.clickToReassign')}
                >
                    {capturingId === row.id ? (row.click ? t('settings.preferences.nowClick') : t('settings.preferences.pressKey')) : row.value === '' ? t('settings.preferences.none') : formatShortcut(row.value ?? row.def)}
                </button>
            </div>
        </div>
    {/each}

    {#if invalidFlash}
        <p class="invalid-hint" role="alert">{invalidKind === 'click' ? t('settings.preferences.invalidClick') : t('settings.preferences.invalidKey')}</p>
    {/if}
</section>

<section class="settings-section">
    <div class="section-header">
        <h3 class="settings-section-title">{t('settings.preferences.ui')}</h3>
    </div>
    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">{t('settings.preferences.hideModelBadge')}</span>
            <span id="hide-model-badge-desc" class="shortcut-desc">{t('settings.preferences.hideModelBadgeDesc')}</span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={hideModelBadge} ariaLabel={t('settings.preferences.hideModelBadge')} ariaDescribedby="hide-model-badge-desc" onChange={() => onDirty?.()} />
        </div>
    </div>
    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">{t('settings.preferences.oldestFirst')}</span>
            <span id="sort-oldest-first-desc" class="shortcut-desc">{t('settings.preferences.oldestFirstDesc')}</span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={sortOldestFirst} ariaLabel={t('settings.preferences.oldestFirst')} ariaDescribedby="sort-oldest-first-desc" onChange={() => onDirty?.()} />
        </div>
    </div>
    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">{t('settings.preferences.autoMaximizeChat')}</span>
            <span id="auto-maximize-chat-desc" class="shortcut-desc">{t('settings.preferences.autoMaximizeChatDesc')}</span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={autoMaximizeChat} ariaLabel={t('settings.preferences.autoMaximizeChat')} ariaDescribedby="auto-maximize-chat-desc" onChange={() => onDirty?.()} />
        </div>
    </div>
    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">{t('settings.preferences.notifyOnReview')}</span>
            <span id="notify-on-review-desc" class="shortcut-desc">
                {notifyOnReview && permission === 'unsupported'
                    ? t('settings.preferences.notifyUnsupported')
                    : notifyOnReview && permission !== 'granted'
                        ? t('settings.preferences.notifyBlocked')
                        : t('settings.preferences.notifyOnReviewDesc')}
            </span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={notifyOnReview} ariaLabel={t('settings.preferences.notifyOnReview')} ariaDescribedby="notify-on-review-desc" onChange={() => void onNotifyChange()} />
        </div>
    </div>
</section>

<style>
    @import '$lib/styles/settings-section.css';

    .section-header {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 8px;
        flex-wrap: wrap;
    }
    .shortcut-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
    }
    .shortcut-info {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
    }
    .shortcut-name {
        font-size: var(--text-base);
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .shortcut-desc {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
    }
    .shortcut-controls {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-shrink: 0;
    }
    .shortcut-action {
        padding: 6px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: var(--text-sm);
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
    }
    .shortcut-action:hover {
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .key-chip {
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: var(--text-sm);
        padding: 6px 12px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        cursor: pointer;
        min-width: 84px;
        text-align: center;
        transition: border-color 0.15s, background 0.15s;
    }
    .key-chip:hover {
        border-color: var(--color-accent);
    }
    .key-chip.capturing {
        border-color: var(--color-accent);
        background: var(--color-surface-1);
        color: var(--color-accent);
        animation: capture-pulse 1.2s ease-in-out infinite;
    }
    .key-chip.invalid {
        border-color: var(--color-warning);
        color: var(--color-warning);
    }
    .invalid-hint {
        margin: -6px 0 0;
        font-size: var(--text-xs);
        color: var(--color-warning);
    }
    @keyframes capture-pulse {
        0%, 100% { opacity: 1; }
        50% { opacity: 0.55; }
    }
</style>
