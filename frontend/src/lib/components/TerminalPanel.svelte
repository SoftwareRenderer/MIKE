<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import { showTerminal, selectedProject, selectedTask } from '$lib/store';
    import { createTerminal, createProjectTerminal } from '$lib/api';
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
    let panelHeight = $state(loadHeight());

    function loadHeight(): number {
        try {
            const stored = localStorage.getItem(HEIGHT_KEY);
            if (stored !== null) {
                const val = Number.parseInt(stored, 10);
                if (!isNaN(val) && val >= MIN_HEIGHT && val <= MAX_HEIGHT) return val;
            }
        } catch {}
        return DEFAULT_HEIGHT;
    }

    function saveHeight(value: number) {
        try {
            localStorage.setItem(HEIGHT_KEY, String(value));
        } catch {}
    }

    function close() {
        trap?.destroy();
        trap = null;
        showTerminal.set(false);
        if (ws) ws.close();
        setTimeout(() => triggerEl?.focus(), 50);
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
                saveHeight(panelHeight);
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
            ws = createTerminal(proj.id, task.id, term);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else if (proj) {
            ws = createProjectTerminal(proj.id, term);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else {
            term.writeln('No project selected');
        }

        const observer = new ResizeObserver(() => {
            fit.fit();
        });
        observer.observe(container);

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') close();
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

<button type="button" class="terminal-overlay" onclick={close} aria-label="Close terminal"></button>
<div class="terminal-panel" bind:this={panelEl} role="dialog" aria-modal="true" aria-label="Terminal" style="height:{panelHeight}px">
    <div class="terminal-header">
        <div class="resize-handle" onmousedown={onResizeStart} role="separator" aria-orientation="horizontal" aria-label="Resize terminal"></div>
        <span class="title">Terminal</span>
        <button class="close-btn" onclick={close} aria-label="Close terminal">✕</button>
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
    .title {
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-secondary);
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
    :global(.terminal) {
        height: 100%;
    }
    :global(.xterm-text) {
        line-height: 1.4 !important;
    }
</style>
