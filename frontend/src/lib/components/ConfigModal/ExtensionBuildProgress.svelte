<script lang="ts" module>
    /** What a card shows while one of its operations streams: the percent it reported, the latest line,
     *  and the failure if the stream ended in one. Nothing here is polled — the operation's own stream
     *  writes it, and the terminal event is what ends it. */
    export interface OperationProgress {
        progress: number;
        message: string;
        error?: string;
    }
</script>

<script lang="ts">
    let { progress = 0, message = '', error = '' }: {
        progress?: number;
        message?: string;
        error?: string;
    } = $props();
</script>

<div class="ext-build" class:ext-build-error={!!error}>
    <div class="ext-build-bar"><div class="ext-build-fill" style="--pct: {progress}%"></div></div>
    <span class="ext-build-text">{error || message}</span>
</div>

<style>
    /* In-card build progress (compact) */
    .ext-build {
        margin: 0 0 0.75rem;
    }

    .ext-build-bar {
        height: 6px;
        background: rgba(88, 166, 255, 0.15);
        border-radius: var(--radius-sm);
        overflow: hidden;
    }

    .ext-build-fill {
        height: 100%;
        width: var(--pct, 0%);
        background: var(--color-accent);
        border-radius: var(--radius-sm);
        transition: width 0.3s ease;
    }

    .ext-build-error .ext-build-fill {
        background: var(--color-status-red);
    }

    .ext-build-text {
        display: block;
        margin-top: 0.25rem;
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
</style>
