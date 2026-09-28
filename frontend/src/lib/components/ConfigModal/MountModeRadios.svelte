<script lang="ts">
    /**
     * The rw / ro choice for one host folder, as one segmented control. A borrowed root is read-only
     * however the manifest asks, so `ro` is offered first-class rather than as an afterthought.
     *
     * `name` is the group the browser uses to pair the two inputs, so it must be unique per row.
     */
    let { name = 'mode', mode = 'rw', onselect }: {
        name?: string;
        mode?: string;
        onselect: (mode: 'rw' | 'ro') => void;
    } = $props();
</script>

<div class="radio-group radio-group-small">
    <label class="radio-label" class:active={mode === 'rw'}>
        <input type="radio" name={name} value="rw" checked={mode === 'rw'} onchange={() => onselect('rw')} />
        <span class="radio-text">rw</span>
    </label>
    <label class="radio-label" class:active={mode === 'ro'}>
        <input type="radio" name={name} value="ro" checked={mode === 'ro'} onchange={() => onselect('ro')} />
        <span class="radio-text">ro</span>
    </label>
</div>

<style>
    /* Radio group (matches SandboxConfigModal) */
    .radio-group {
        display: flex;
        gap: 0;
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-sm, 4px);
        overflow: hidden;
        flex-shrink: 0;
    }

    .radio-label {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 10px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary, var(--color-text-tertiary));
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
        user-select: none;
    }

    .radio-label:first-child {
        border-right: 1px solid var(--color-border-subtle);
    }

    .radio-label.active {
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }

    .radio-label:hover:not(.active) {
        background: var(--color-surface-3);
    }

    .radio-label input[type="radio"] {
        display: none;
    }

    .radio-text {
        white-space: nowrap;
    }

    .radio-group-small {
        border-radius: var(--radius-sm, 4px);
    }

    .radio-group-small .radio-label {
        padding: 2px 7px;
        font-size: var(--text-sm);
    }
</style>
