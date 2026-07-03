<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import { WebglAddon } from '@xterm/addon-webgl';
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
    let kbContainer: HTMLElement | null = null;
    let ws: WebSocket | null = null;
    let panelEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let panelHeight = $state(DEFAULT_HEIGHT);
    let modCtrl = $state(false);
    let modAlt = $state(false);

    /** Escape sequences sent by xterm.js for each key. */
    const KEY_SEQ: Record<string, string> = {
        Escape: '\x1b',
        Tab: '\x09',
        Home: '\x1b[H',
        End: '\x1b[F',
        PageUp: '\x1b[5~',
        PageDown: '\x1b[6~',
        ArrowUp: '\x1b[A',
        ArrowDown: '\x1b[B',
        ArrowLeft: '\x1b[D',
        ArrowRight: '\x1b[C',
    };

    /** Send raw key data directly over the WebSocket (bypasses xterm focus requirement). */
    function sendKeyData(data: string) {
        if (ws?.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'input', data }));
        }
    }

    /** Handle a shortcut key press. Sends escape sequence via WebSocket. */
    function handleKey(key: string) {
        const ctrl = modCtrl;
        const alt = modAlt;
        modCtrl = false;
        modAlt = false;

        // Ctrl + single char → raw control character
        if (ctrl && key.length === 1) {
            const code = key.toUpperCase().charCodeAt(0) - 64;
            if (code >= 0 && code <= 31) {
                sendKeyData(String.fromCharCode(code));
                return;
            }
        }

        const seq = KEY_SEQ[key] ?? key;
        // Alt prefix: \x1b before the sequence
        sendKeyData((alt ? '\x1b' : '') + seq);
    }

    /** Toggle a modifier key (ctrl/alt). */
    function toggleModifier(mod: 'ctrl' | 'alt') {
        if (mod === 'ctrl') modCtrl = !modCtrl;
        else modAlt = !modAlt;
    }

    /** Transform a character with active modifiers (for virtual keyboard input). */
    function transformInput(data: string): string {
        if (modCtrl && data.length === 1) {
            const code = data.toUpperCase().charCodeAt(0) - 64;
            if (code >= 0 && code <= 31) {
                modCtrl = false;
                return String.fromCharCode(code);
            }
        }
        if (modAlt) {
            modAlt = false;
            return '\x1b' + data;
        }
        return data;
    }

    // Block focus changes inside the keyboard panel (keeps virtual keyboard open).
    $effect(() => {
        if (!kbContainer) return;
        const handler = (e: PointerEvent) => e.preventDefault();
        kbContainer.addEventListener('pointerdown', handler, { capture: true });
        return () => kbContainer.removeEventListener('pointerdown', handler, { capture: true });
    });

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

        let webglRef: WebglAddon | null = new WebglAddon();
        webglRef.onContextLoss(() => { webglRef = null; });
        term.loadAddon(webglRef);
        const fit = new FitAddon();
        term.loadAddon(fit);
        term.open(container);
        fit.fit();
        term.focus();

        const proj = get(selectedProject);
        const task = get(selectedTask);
        if (proj && task) {
            ws = createTerminal(proj.id, task.id, term, close, transformInput);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else if (proj) {
            ws = createProjectTerminal(proj.id, term, close, transformInput);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else {
            term.writeln('No project selected');
        }

        let fitPending = false;
        const observer = new ResizeObserver(() => {
            if (!fitPending) {
                fitPending = true;
                requestAnimationFrame(() => { fit.fit(); fitPending = false; });
            }
        });
        observer.observe(container);

        // Enable inertial scrolling on mobile.
        // Tracks per-frame deltas, preserves fractional line movements, and normalizes decay across refresh rates.
        let touchStartY = 0;
        let touchVelSamples: { y: number; t: number }[] = [];
        let inertiaRaf: number | null = null;
        let currentTouchY = 0;
        let pixelBufferY = 0; // Remainder buffer to prevent quantization jank
        let lastTickTime = 0;
        let isDisposed = false;

        const LINE_HEIGHT = 16; // Match your terminal's row height

        const stopInertia = () => {
            if (inertiaRaf) { cancelAnimationFrame(inertiaRaf); inertiaRaf = null; }
        };

        const onTouchStart = (e: TouchEvent) => {
            if (isDisposed) return;
            stopInertia();
            
            touchStartY = e.touches[0].pageY;
            currentTouchY = touchStartY;
            pixelBufferY = 0; 
            touchVelSamples = [{ y: touchStartY, t: performance.now() }];
        };

        const onTouchMove = (e: TouchEvent) => {
            if (isDisposed) return;
            
            const now = performance.now();
            currentTouchY = e.touches[0].pageY;
            
            // 1. Log sample for inertia velocity calculation
            touchVelSamples.push({ y: currentTouchY, t: now });
            while (touchVelSamples.length > 1 && touchVelSamples[0].t < now - 100) {
                touchVelSamples.shift();
            }
            
            // 2. Track precise delta instead of resetting to a strict grid
            const deltaY = touchStartY - currentTouchY;
            touchStartY = currentTouchY;
            
            pixelBufferY += deltaY;
            
            // 3. Only scroll when accumulated pixels cross a full line threshold
            if (Math.abs(pixelBufferY) >= LINE_HEIGHT) {
                const lines = Math.trunc(pixelBufferY / LINE_HEIGHT);
                pixelBufferY -= lines * LINE_HEIGHT; // Save the fractional remainder!
                try { term.scrollLines(lines); } catch {}
            }
        };

        const onTouchEnd = () => {
            if (isDisposed) return;
            if (touchVelSamples.length < 3) return;
            
            let totalVel = 0, weights = 0;
            for (let i = 1; i < touchVelSamples.length; i++) {
                const dt = touchVelSamples[i].t - touchVelSamples[i-1].t || 1;
                const vel = (touchVelSamples[i-1].y - touchVelSamples[i].y) / dt;
                const w = i / touchVelSamples.length;
                totalVel += vel * w;
                weights += w;
            }
            
            const velocity = totalVel / weights;
            if (Math.abs(velocity) < 0.15) return;
            
            const speed = Math.abs(velocity);
            const scaled = speed * Math.pow(speed / 0.3, 0.8);
            let remaining = scaled * 8 * Math.sign(velocity);
            
            lastTickTime = performance.now();
            
            const tick = (now: number) => {
                if (isDisposed) return;
                
                // Calculate dynamic decay based on elapsed time (targets 60Hz baseline)
                const dt = now - lastTickTime;
                lastTickTime = now;
                const currentDecay = Math.pow(0.93, dt / 16.67);
                
                remaining *= currentDecay;
                if (Math.abs(remaining) < 0.1) return;
                
                // Apply inertia into the exact same pixel buffer
                pixelBufferY += remaining;
                
                if (Math.abs(pixelBufferY) >= LINE_HEIGHT) {
                    const lines = Math.trunc(pixelBufferY / LINE_HEIGHT);
                    pixelBufferY -= lines * LINE_HEIGHT;
                    try { term.scrollLines(lines); } catch {}
                }
                
                inertiaRaf = requestAnimationFrame(tick);
            };
            
            inertiaRaf = requestAnimationFrame(tick);
        };

        term.element.addEventListener('touchstart', onTouchStart, { passive: true });
        term.element.addEventListener('touchmove', onTouchMove, { passive: true });
        term.element.addEventListener('touchend', onTouchEnd, { passive: true });

        function handleKeyDown(e: KeyboardEvent) {
            if (e.key === 'Escape') close();
        }
        window.addEventListener('keydown', handleKeyDown);

        return () => {
            isDisposed = true;
            fitPending = false;
            stopInertia();
            if (ws) ws.close();
            ws = null;
            observer.disconnect();
            window.removeEventListener('keydown', handleKeyDown);
            webglRef = null;
            try { term?.dispose(); } catch {}
            term = null;
        };
    });
</script>

<button type="button" class="terminal-overlay" onclick={close} aria-label="Close terminal" title="Close terminal"></button>
<div class="terminal-panel" bind:this={panelEl} role="dialog" aria-modal="true" aria-label="Terminal" style="height:{panelHeight}px">
    <div class="terminal-header">
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div class="resize-handle" onmousedown={onResizeStart} role="separator" aria-orientation="horizontal" aria-label="Resize terminal"></div>
        <span class="title">Terminal</span>
        <div class="header-actions">
            <button class="close-btn" onclick={close} aria-label="Close terminal" title="Close terminal">✕</button>
        </div>
    </div>
    <div class="terminal-container" bind:this={container}></div>
    <div class="mobile-keyboard" bind:this={kbContainer}>
        <div class="kb-row">
            <div role="button" class="kb-btn" onclick={() => handleKey('Escape')}>esc</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('/')}>/</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('-')}>-</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('Home')}>home</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('ArrowUp')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 4l4 5H4l4-5z" fill="currentColor"/></svg>
            </div>
            <div role="button" class="kb-btn" onclick={() => handleKey('End')}>end</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('PageUp')}>pgup</div>
        </div>
        <div class="kb-row">
            <div role="button" class="kb-btn" onclick={() => handleKey('Tab')}>tab</div>
            <div role="button" class="kb-btn kb-mod" class:kb-mod-active={!!modCtrl} onclick={() => toggleModifier('ctrl')}>ctrl</div>
            <div role="button" class="kb-btn kb-mod" class:kb-mod-active={!!modAlt} onclick={() => toggleModifier('alt')}>alt</div>
            <div role="button" class="kb-btn" onclick={() => handleKey('ArrowLeft')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 8l5-4v8l-5-4z" fill="currentColor"/></svg>
            </div>
            <div role="button" class="kb-btn" onclick={() => handleKey('ArrowDown')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 12l-4-5h8l-4 5z" fill="currentColor"/></svg>
            </div>
            <div role="button" class="kb-btn" onclick={() => handleKey('ArrowRight')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M12 8l-5 4V4l5 4z" fill="currentColor"/></svg>
            </div>
            <div role="button" class="kb-btn" onclick={() => handleKey('PageDown')}>pgdn</div>
        </div>
    </div>
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
    /* Mobile keyboard shortcut panel */
    .mobile-keyboard {
        display: none;
    }
    @media (max-width: 1024px) {
        /* Override inline pixel height — use dynamic viewport so panel
           never exceeds the visible area when the address bar hides. */
        .terminal-panel {
            height: 100dvh !important;
        }
        .mobile-keyboard {
            position: absolute;
            bottom: 0;
            left: 0;
            right: 0;
            z-index: 10;
            display: flex;
            flex-direction: column;
            gap: 4px;
            padding: 4px 8px;
            background: var(--color-surface-1);
            border-top: 1px solid var(--color-border);
            user-select: none;
            -webkit-user-select: none;
            touch-action: manipulation;
        }
        .kb-row {
            display: flex;
            gap: 3px;
        }
        .kb-btn {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            height: 36px;
            background: var(--color-surface-3);
            border: 1px solid var(--color-border);
            border-radius: var(--radius-sm);
            color: var(--color-text-primary);
            font-family: var(--font-mono);
            font-size: 0.7rem;
            font-weight: 500;
            text-transform: uppercase;
            letter-spacing: 0.03em;
            cursor: pointer;
            transition: background 0.1s, border-color 0.1s, color 0.1s;
            -webkit-tap-highlight-color: transparent;
        }
        .kb-btn:active {
            background: #555;
            border-color: #888;
            color: #fff;
        }
        .kb-mod {
            background: var(--color-surface-4);
        }
        .kb-mod-active {
            background: var(--color-accent) !important;
            border-color: var(--color-accent) !important;
            color: #fff !important;
        }
        /* Push the terminal viewport up so its bottom doesn't sit behind
           the keyboard panel (~75px tall). flex: 1 + margin-bottom in a
           flex column shrinks the flex child by the margin amount. */
        .terminal-container {
            margin-bottom: 85px;
        }
    }
    :global(.terminal) {
        height: 100%;
    }
    :global(.xterm-text) {
        line-height: 1.4 !important;
    }
    /* Touch-scroll the terminal viewport on mobile */
    :global(.xterm-viewport) {
        touch-action: pan-y;
        -webkit-overflow-scrolling: touch;
    }
</style>
