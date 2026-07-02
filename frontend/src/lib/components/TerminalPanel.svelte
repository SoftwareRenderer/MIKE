<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import { showTerminal, selectedProject, selectedTask } from '$lib/store';
    import { createTerminal, createProjectTerminal } from '$lib/api';
    import { loadAppSetting, saveAppSetting } from '$lib/storage';
    import { createFocusTrap } from '$lib/a11y';
    import { startDrag } from '$lib/utils/use-resize-drag';
    import '@xterm/xterm/css/xterm.css';

    const HEIGHT_KEY = 'kanban:terminal:height';
    const DEFAULT_HEIGHT = 360;
    const MIN_HEIGHT = 180;
    const MAX_HEIGHT = 800;

    let term: Terminal | null = null;
    let container: HTMLElement | null = null;
    let ws: WebSocket | null = null;
    let panelEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let panelHeight = $state(DEFAULT_HEIGHT);
    let terminalFullscreen = $state(false);

    /** Load saved height from IndexedDB. */
    async function loadHeight(): Promise<number> {
        const stored = await loadAppSetting(HEIGHT_KEY, DEFAULT_HEIGHT);
        const val = Number.parseInt(String(stored), 10);
        if (!isNaN(val) && val >= MIN_HEIGHT && val <= MAX_HEIGHT) return val;
        return DEFAULT_HEIGHT;
    }

    /** Save height to IndexedDB. */
    async function saveHeight(value: number): Promise<void> {
        await saveAppSetting(HEIGHT_KEY, value);
    }

    // Load saved height on init
    loadHeight().then(h => { panelHeight = h; }).catch(() => {});

    function close() {
        trap?.destroy();
        trap = null;
        showTerminal.set(false);
        if (ws) ws.close();
        ws = null;
        setTimeout(() => triggerEl?.focus(), 50);
    }

    function toggleFullscreen() {
        terminalFullscreen = !terminalFullscreen;
        // Resize terminal after transition
        setTimeout(() => {
            const fitAddon = term?.getAddon(FitAddon);
            fitAddon?.fit?.();
        }, 50);
    }

    function onResizeStart(event: MouseEvent) {
        const startY = event.clientY;
        const startHeight = panelHeight;

        startDrag(event, {
            axis: 'y',
            cursor: 'ns-resize',
            onMove: (pointerY: number) => {
                const delta = startY - pointerY; // dragging up increases height
                panelHeight = Math.max(MIN_HEIGHT, Math.min(MAX_HEIGHT, startHeight + delta));
            },
            onEnd: () => {
                saveHeight(panelHeight).catch(() => {});
            },
        });
    }

    onMount(() => {
        if (!container) return;
        triggerEl = document.activeElement as HTMLElement;
        if (panelEl) {
            trap = createFocusTrap(panelEl, close);
        }

        term = new Terminal({
            cursorBlink: true,
            fontSize: 13,
            lineHeight: 1.4,
            fontFamily: '"Noto Sans Mono", ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
            theme: {
                background: '#1F2428',
                foreground: '#E6EDF3',
                cursor: '#0084FF',
                selectionBackground: '#3E464E',
            },
        });

        const fit = new FitAddon();
        term.loadAddon(fit);
        term.open(container);
        fit.fit();
        term.focus();

        const proj = get(selectedProject);
        const task = get(selectedTask);
        if (proj && task) {
            ws = createTerminal(proj.id, task.id, term, close);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else if (proj) {
            ws = createProjectTerminal(proj.id, term, close);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else {
            term.writeln('No project selected');
        }

        const observer = new ResizeObserver(() => {
            fit.fit();
        });
        observer.observe(container);

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') {
                if (terminalFullscreen) {
                    terminalFullscreen = false;
                    setTimeout(() => { const fa = term?.getAddon(FitAddon); fa?.fit?.(); }, 50);
                } else {
                    close();
                }
            }
        }
        window.addEventListener('keydown', handleKey);

        return () => {
            observer.disconnect();
            window.removeEventListener('keydown', handleKey);
            if (ws) ws.close();
            term?.dispose();
        };
    });
</script>

<button type="button" class="terminal-overlay" onclick={close} aria-label="Close terminal" title="Close terminal"></button>
<div class="terminal-panel" class:fullscreen={terminalFullscreen} bind:this={panelEl} role="dialog" aria-modal="true" aria-label="Terminal" style="height:{panelHeight}px">
    <div class="terminal-header">
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div class="resize-handle" onmousedown={onResizeStart} role="separator" aria-orientation="horizontal" aria-label="Resize terminal"></div>
        <span class="title">Terminal</span>
        <div class="header-actions">
            <button type="button" class="icon-btn fullscreen-btn" onclick={toggleFullscreen} aria-label="Toggle fullscreen" title="Toggle fullscreen">
                {#if terminalFullscreen}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                {/if}
            </button>
            <button class="close-btn" onclick={close} aria-label="Close terminal" title="Close terminal">✕</button>
        </div>
    </div>
    <div class="terminal-container" bind:this={container}></div>
</div>

<style>
    .terminal-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.3);
        z-index: 199;
        border: none;
        padding: 0;
        cursor: pointer;
        color: transparent;
        font-size: 0;
    }
    .terminal-panel {
        position: fixed;
        bottom: 0;
        left: 0;
        right: 0;
        background: var(--color-surface-0);
        border-top: 1px solid var(--color-border);
        z-index: 200;
        display: flex;
        flex-direction: column;
    }
    .terminal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 4px 12px;
        background: var(--color-surface-2);
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        position: relative;
    }
    .resize-handle {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        height: 8px;
        cursor: ns-resize;
        z-index: 1;
    }
    .resize-handle::after {
        content: '';
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        height: 2px;
        background: var(--color-border);
        transition: background 0.15s ease;
    }
    .resize-handle:hover::after {
        background: var(--color-accent);
    }
    .title {
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-secondary);
    }
    .header-actions {
        display: flex;
        align-items: center;
        gap: 2px;
    }
    .icon-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
    }
    .icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    /* Fullscreen toggle: visible only on mobile */
    .fullscreen-btn {
        display: none;
    }
    @media (max-width: 768px) {
        .fullscreen-btn {
            display: flex;
        }
    }
    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: 1rem;
        cursor: pointer;
        padding: 2px 6px;
    }
    .terminal-container {
        flex: 1;
        padding: 4px;
        min-height: 0;
    }
    /* Fullscreen mode: expand to cover entire viewport */
    .terminal-panel.fullscreen {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        height: 100vh !important;
        z-index: 300;
        border-radius: 0;
    }
    .terminal-panel.fullscreen .resize-handle {
        display: none;
    }
    :global(.terminal) {
        height: 100%;
    }
    :global(.xterm-text) {
        line-height: 1.4 !important;
    }
</style>
