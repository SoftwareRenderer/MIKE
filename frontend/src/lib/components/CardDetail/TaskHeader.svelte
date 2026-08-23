<script lang="ts">
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import { getWorktreeDisplayPath } from '$lib/utils/path';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import SandboxPanel from '../SandboxPanel.svelte';
    import type { Task } from '$lib/types';
    import { appStore } from '$lib/stores';
    import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
    import { isSandboxMemoryWarning } from '$lib/utils/memory-stats';
    import { effectiveOpenTerminalKey, formatShortcut } from '$lib/utils/keyboard';

    let {
        workspaceDir,
        taskId,
        sandboxEnabled,
        badge,
        onClose,
        onOpenTerminal,
        onOpenExplorer,
        onDelete,
        task,
    }: {
        workspaceDir: string | undefined;
        taskId: string;
        sandboxEnabled: boolean | undefined;
        badge: { text: string; color: string };
        onClose: (status?: string) => void;
        onOpenTerminal: () => void;
        onOpenExplorer: () => void;
        onDelete: () => Promise<void>;
        task: Task;
    } = $props();

    let showSandboxPanel = $state(false);

    // Live sandbox memory stats for this task (WS-pushed; the most recent
    // value persists while the task is idle).
    const stats = $derived(sandboxStatsStore.get(task.id));
    const memWarning = $derived(isSandboxMemoryWarning(stats));

    // Sandbox button tooltip — live usage + OOM kills when known.
    const sandboxTitle = $derived.by(() => {
        let title = 'Sandbox enabled';
        if (stats?.running) {
            title += stats.memory_limit_mb > 0
                ? ` · ${stats.memory_current_mb} / ${stats.memory_limit_mb} MB`
                : ` · ${stats.memory_current_mb} MB`;
        }
        if (stats && stats.oom_kills > 0) {
            title += ` · ${stats.oom_kills} OOM kill${stats.oom_kills > 1 ? 's' : ''}`;
        }
        return `${title} (click for details)`;
    });

    // Terminal button tooltip — always shows the effective shortcut (tooltips
    // keep the shortcut text even when keycap hints are hidden).
    const terminalButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key ? `Open terminal (${formatShortcut(key)})` : 'Open terminal';
    });

    // Full worktree display path shown in the header (CSS-ellipsized when it
    // doesn't fit; the native `title` below only appears on hover).
    const fullPath = $derived(getWorktreeDisplayPath(workspaceDir || '', task.project_id, taskId));

    // Mobile: tapping the (truncated) path shows the full path as a tooltip —
    // native `title` tooltips never trigger on touch. Auto-hides after a few
    // seconds; outside taps and viewport changes dismiss it early.
    const isMobileCtx = useIsMobile();
    let pathTip = $state<{ top: number; left: number } | null>(null);
    let pathTipTimer: ReturnType<typeof setTimeout> | null = null;
    let pathSpanEl = $state<HTMLSpanElement | null>(null);
    let pathTipEl = $state<HTMLDivElement | null>(null);

    const PATH_TIP_VISIBLE_MS = 2500;
    const PATH_TIP_MARGIN = 8;

    function clearPathTip() {
        if (pathTipTimer) {
            clearTimeout(pathTipTimer);
            pathTipTimer = null;
        }
        document.removeEventListener('pointerdown', handleOutsidePointerDown, true);
        window.removeEventListener('resize', handlePathTipViewportChange);
        window.removeEventListener('orientationchange', handlePathTipViewportChange);
        pathTip = null;
    }

    function handleOutsidePointerDown(e: PointerEvent) {
        // pointerdown fires before the span's click — ignore taps on the
        // label itself so re-tapping toggles the tooltip off (via the click
        // handler) instead of close-then-reopen.
        if (pathSpanEl && e.target instanceof Node && pathSpanEl.contains(e.target)) return;
        clearPathTip();
    }

    // The tooltip is positioned with fixed pixel coordinates — any viewport
    // change (rotation, resize) would leave it anchored to stale geometry.
    function handlePathTipViewportChange() {
        clearPathTip();
    }

    function handlePathTap() {
        if (!isMobileCtx.isMobile || !fullPath) return;
        if (pathTip) {
            clearPathTip();
            return;
        }
        const rect = pathSpanEl?.getBoundingClientRect();
        if (!rect) return;

        // Anchor below the label (the header is the topmost row, so there is
        // no room above), left-aligned to it, clamped to the viewport.
        const estWidth = Math.min(360, window.innerWidth * 0.8);
        const left = Math.max(PATH_TIP_MARGIN, Math.min(rect.left, window.innerWidth - PATH_TIP_MARGIN - estWidth));
        pathTip = { top: rect.bottom + 6, left };

        pathTipTimer = setTimeout(clearPathTip, PATH_TIP_VISIBLE_MS);
        document.addEventListener('pointerdown', handleOutsidePointerDown, true);
        window.addEventListener('resize', handlePathTipViewportChange);
        window.addEventListener('orientationchange', handlePathTipViewportChange);

        // The width estimate may not match the rendered (wrapped) width —
        // clamp against the real box after layout.
        requestAnimationFrame(() => {
            const tip = pathTipEl;
            if (!tip || !pathTip) return;
            const box = tip.getBoundingClientRect();
            if (box.right > window.innerWidth - PATH_TIP_MARGIN) {
                tip.style.left = `${Math.max(PATH_TIP_MARGIN, window.innerWidth - PATH_TIP_MARGIN - box.width)}px`;
            } else if (box.left < PATH_TIP_MARGIN) {
                tip.style.left = `${PATH_TIP_MARGIN}px`;
            }
        });
    }

    // Release tooltip listeners when the header unmounts (task view closed).
    $effect(() => clearPathTip);
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
                    <button class="icon-btn sandbox-btn" onclick={() => { showSandboxPanel = true; }} title={sandboxTitle} aria-label={sandboxTitle}>
                        <Icon src={shieldIcon} width={14} height={14} />
                        {#if memWarning}
                            <span class="mem-dot" aria-hidden="true"></span>
                        {/if}
                    </button>
                {:else}
                    <span class="sandbox-badge" title="Sandbox disabled">
                        <Icon src={shieldOffIcon} color="var(--color-status-red)" width={14} height={14} />
                    </span>
                {/if}
            {/if}
            <!-- svelte-ignore a11y_click_events_have_key_events — mobile-only tap target on the label (no keyboard equivalent; desktop uses the native title tooltip) -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <span class="project-prefix" bind:this={pathSpanEl} title={fullPath} aria-describedby={pathTip ? 'task-header-path-tooltip' : undefined} onclick={handlePathTap}>{fullPath}</span>
            <span class="badge" style="background:{badge.color}18;color:{badge.color};border-color:{badge.color}40">
                {badge.text}
            </span>
        </div>
    </div>
    <div class="header-actions">
        <button class="icon-btn" onclick={onOpenExplorer} aria-label="Open explorer" title="Open explorer">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M2 3h4l1.5 2H13l1.5 2.5V13a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
            </svg>
        </button>
        <button class="icon-btn" onclick={onOpenTerminal} aria-label="Open terminal" title={terminalButtonTitle}>
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
    <SandboxPanel {task} {stats} onClose={() => { showSandboxPanel = false; }} />
{/if}

{#if pathTip}
    <div id="task-header-path-tooltip" class="path-tooltip" role="tooltip" bind:this={pathTipEl} style:top="{pathTip.top}px" style:left="{pathTip.left}px">{fullPath}</div>
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
    .sandbox-btn {
        position: relative;
    }
    .mem-dot {
        position: absolute;
        top: 4px;
        right: 4px;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--color-status-red);
        pointer-events: none;
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
    /* Mobile full-path tooltip — fixed so it escapes the label's overflow:
       hidden truncation; pointer-events off so taps pass through (an outside
       tap dismisses it). */
    .path-tooltip {
        position: fixed;
        z-index: 9000;
        max-width: min(80vw, 360px);
        padding: 6px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.4;
        color: var(--color-text-primary);
        word-break: break-all;
        white-space: normal;
        pointer-events: none;
    }
</style>
