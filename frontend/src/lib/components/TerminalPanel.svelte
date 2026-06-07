
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import { showTerminal, selectedProject } from '$lib/store';
    import { createTerminal } from '$lib/api';
    import { createFocusTrap } from '$lib/a11y';
    import '@xterm/xterm/css/xterm.css';

    let term: Terminal | null = null;
    let container: HTMLElement | null = null;
    let ws: WebSocket | null = null;
    let panelEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;

    function close() {
        trap?.destroy();
        trap = null;
        showTerminal.set(false);
        if (ws) ws.close();
        setTimeout(() => triggerEl?.focus(), 50);
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
            fontFamily: 'var(--font-mono)',
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

        const proj = selectedProject.get();
        if (proj) {
            ws = createTerminal(proj.id, term);
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
<div class="terminal-panel" bind:this={panelEl} role="dialog" aria-modal="true" aria-label="Terminal">
    <div class="terminal-header">
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
        height: 360px;
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
        padding: 6px 12px;
        background: var(--color-surface-2);
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
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
    }
    :global(.terminal) {
        height: 100%;
    }
</style>