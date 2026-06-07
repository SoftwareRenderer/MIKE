
<script lang="ts">
    let { tokens, onCopy, onToggleRaw }: {
        tokens: { prompt: number; completed: number; total: number; duration: number };
        onCopy?: () => void;
        onToggleRaw?: () => void;
    } = $props();

    let copied = $state(false);

    function copy() {
        onCopy?.();
        copied = true;
        setTimeout(() => copied = false, 2000);
    }

    function toggleRaw() {
        onToggleRaw?.();
    }

    function formatDuration(ms: number): string {
        if (ms < 1000) return `${ms}ms`;
        const s = ms / 1000;
        if (s < 60) return `${s.toFixed(1)}s`;
        return `${(s / 60).toFixed(1)}m`;
    }
</script>

<div class="stream-actions">
    <button class="action-btn" onclick={copy} aria-label="Copy output">
        {#if copied}✓ Copied{:else}📋 Copy{/if}
    </button>
    <button class="action-btn" onclick={toggleRaw} aria-label="Toggle raw view">
        Raw
    </button>
    {#if tokens.total > 0}
        <div class="stats">
            <span class="stat" title="Prompt tokens">{tokens.prompt} prompt</span>
            <span class="stat" title="Completed tokens">{tokens.completed} generated</span>
            <span class="stat" title="Duration">{formatDuration(tokens.duration)}</span>
        </div>
    {/if}
</div>

<style>
    .stream-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 12px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-0);
        flex-shrink: 0;
    }
    .action-btn {
        padding: 4px 10px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        border-radius: var(--radius-sm);
        font-size: 12px;
        cursor: pointer;
    }
    .action-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .stats {
        display: flex;
        gap: 12px;
        margin-left: auto;
    }
    .stat {
        font-size: 11px;
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
    }
</style>