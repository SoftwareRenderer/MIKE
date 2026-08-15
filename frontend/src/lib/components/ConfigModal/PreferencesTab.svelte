<script lang="ts">
    import { DEFAULT_SHORTCUTS, normalizeKey, formatShortcut, defaultCreateTaskSubmitKey } from '$lib/utils/keyboard';
    import ToggleSwitch from '../ToggleSwitch.svelte';

    let {
        createTaskKey = $bindable<string | undefined>(),
        createTaskSubmitKey = $bindable<string | undefined>(),
        hideHints = $bindable(false),
        onDirty,
    }: {
        createTaskKey?: string | undefined;
        createTaskSubmitKey?: string | undefined;
        hideHints?: boolean;
        onDirty?: () => void;
    } = $props();

    // One row per shortcut; the submit default is platform-aware (Cmd+Enter on Mac).
    const rows = $derived([
        {
            id: 'create_task' as const,
            name: 'Create Task',
            desc: 'Opens the create-task dialog on the project board',
            value: createTaskKey,
            def: DEFAULT_SHORTCUTS.create_task,
        },
        {
            id: 'create_task_submit' as const,
            name: 'Create Task (submit)',
            desc: 'Creates the task inside the create-task dialog',
            value: createTaskSubmitKey,
            def: defaultCreateTaskSubmitKey(),
        },
    ]);

    let capturingId = $state<string | null>(null);
    let invalidFlash = $state(false);
    let invalidTimer: ReturnType<typeof setTimeout> | undefined;

    function assign(id: string, value: string | undefined) {
        if (id === 'create_task') createTaskKey = value;
        else createTaskSubmitKey = value;
        onDirty?.();
    }

    function startCapture(id: string) {
        // Click again to cancel.
        capturingId = capturingId === id ? null : id;
        invalidFlash = false;
    }

    $effect(() => {
        const id = capturingId;
        if (!id) return;
        const handleKeydown = (e: KeyboardEvent) => {
            e.preventDefault();
            e.stopPropagation();
            if (e.key === 'Escape') {
                capturingId = null;
                return;
            }
            const normalized = normalizeKey(e);
            if (!normalized) {
                invalidFlash = true;
                if (invalidTimer) clearTimeout(invalidTimer);
                invalidTimer = setTimeout(() => {
                    invalidFlash = false;
                    invalidTimer = undefined;
                }, 1500);
                return;
            }
            assign(id, normalized);
            capturingId = null;
        };
        window.addEventListener('keydown', handleKeydown, true);
        return () => {
            window.removeEventListener('keydown', handleKeydown, true);
            if (invalidTimer) clearTimeout(invalidTimer);
        };
    });
</script>

<section class="section">
    <div class="section-header">
        <h3 class="section-label">Keyboard Shortcuts</h3>
    </div>

    <div class="shortcut-row">
        <div class="shortcut-info">
            <span class="shortcut-name">Hide shortcut hints</span>
            <span id="hide-hints-desc" class="shortcut-desc">Hide the keycaps shown on buttons</span>
        </div>
        <div class="shortcut-controls">
            <ToggleSwitch bind:checked={hideHints} ariaLabel="Hide shortcut hints" ariaDescribedby="hide-hints-desc" onChange={() => onDirty?.()} />
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
                    <button type="button" class="shortcut-action" onclick={() => assign(row.id, undefined)} aria-label="Reset to default" title={`Reset to default (${formatShortcut(row.def)})`}>Reset</button>
                {/if}
                {#if row.value ?? row.def}
                    <button type="button" class="shortcut-action" onclick={() => assign(row.id, '')} aria-label="Clear shortcut" title="Disable this shortcut">Clear</button>
                {/if}
                <button
                    type="button"
                    class="key-chip"
                    class:capturing={capturingId === row.id}
                    class:invalid={invalidFlash}
                    onclick={() => startCapture(row.id)}
                    aria-label={`${row.name} shortcut: ${capturingId === row.id ? 'capturing' : row.value === '' ? 'disabled' : formatShortcut(row.value ?? row.def)}. Click to reassign.`}
                    title={capturingId === row.id ? 'Press any key (Escape to cancel)' : 'Click to reassign'}
                >
                    {capturingId === row.id ? 'Press a key...' : row.value === '' ? 'None' : formatShortcut(row.value ?? row.def)}
                </button>
            </div>
        </div>
    {/each}

    {#if invalidFlash}
        <p class="invalid-hint" role="alert">Invalid key — use a letter or a modifier combo.</p>
    {/if}
</section>

<style>
    .section {
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .section-header {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 8px;
        flex-wrap: wrap;
    }
    .section-label {
        margin: 0;
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
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
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .shortcut-desc {
        font-size: 0.75rem;
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
        font-size: 0.8125rem;
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
    }
    .shortcut-action:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .key-chip {
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: 0.8125rem;
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
        border-color: var(--color-status-orange, #d29922);
        color: var(--color-status-orange, #d29922);
    }
    .invalid-hint {
        margin: -6px 0 0;
        font-size: 0.75rem;
        color: var(--color-status-orange, #d29922);
    }
    @keyframes capture-pulse {
        0%, 100% { opacity: 1; }
        50% { opacity: 0.55; }
    }
</style>
