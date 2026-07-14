<script lang="ts">
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import { getWorktreeDisplayPath } from '$lib/utils/path';
    import SandboxPanel from '../SandboxPanel.svelte';
    import type { Task } from '$lib/types';

    let {
        workspaceDir,
        taskId,
        sandboxEnabled,
        badge,
        worktreeExists,
        onClose,
        onOpenTerminal,
        onDelete,
        task,
    }: {
        workspaceDir: string | undefined;
        taskId: string;
        sandboxEnabled: boolean | undefined;
        badge: { text: string; color: string };
        worktreeExists: boolean;
        onClose: (status?: string) => void;
        onOpenTerminal: () => void;
        onDelete: () => Promise<void>;
        task: Task;
    } = $props();

    let showSandboxPanel = $state(false);
</script>

<div class="header">
    <div class="header-left">
        <button class="btn-back" onclick={() => onClose(task.status)} aria-label="Back to board (Esc)" title="Back to board (Esc)">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M10 3l-5 5 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
        <div class="header-main">
            {#if sandboxEnabled !== undefined}
                {#if sandboxEnabled}
                    <button class="icon-btn" onclick={() => { showSandboxPanel = true; }} title="Sandbox enabled (click for details)">
                        <Icon src={shieldIcon} color="#E6EDF3" width={14} height={14} />
                    </button>
                {:else}
                    <span class="sandbox-badge" title="Sandbox disabled">
                        <Icon src={shieldOffIcon} color="#F85149" width={14} height={14} />
                    </span>
                {/if}
            {/if}
            <span class="project-prefix" title={getWorktreeDisplayPath(workspaceDir || '', taskId)}>{getWorktreeDisplayPath(workspaceDir || '', taskId)}</span>
            <span class="badge" style="background:{badge.color}18;color:{badge.color};border-color:{badge.color}40">
                {badge.text}
            </span>
        </div>
    </div>
    <div class="header-actions">
        <button class="icon-btn" onclick={onOpenTerminal} disabled={!worktreeExists} aria-label="Open terminal" title="Open terminal">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
        <button class="icon-btn" onclick={onDelete} aria-label="Delete task" title="Delete task">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>
</div>

{#if showSandboxPanel}
    <SandboxPanel {task} onClose={() => { showSandboxPanel = false; }} />
{/if}

<style>
    .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        gap: 12px;
        background: var(--color-surface-1);
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .btn-back {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 6px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .btn-back:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .header-main {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .project-prefix {
        font-size: 0.875rem;
        font-weight: 400;
        color: var(--color-text-secondary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1 1 0;
        min-width: 0;
    }
    .badge {
        padding: 2px 8px;
        border-radius: 10px;
        font-size: 0.75rem;
        font-weight: 600;
        flex-shrink: 0;
        border: 1px solid;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .sandbox-badge {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        padding: 6px;
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
    }
    .header-actions {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .icon-btn {
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: 0.875rem;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        border: none;
        cursor: pointer;
        color: inherit;
    }
    .icon-btn:hover:not(:disabled) {
        background: var(--color-accent-dark);
    }
    .icon-btn:disabled {
        opacity: 0.35;
        cursor: not-allowed;
    }
</style>
