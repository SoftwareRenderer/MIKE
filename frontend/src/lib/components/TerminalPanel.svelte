<script lang="ts">
    import { Z } from '$lib/constants/z-layers';
    import { terminalTheme } from '$lib/constants/terminal-theme';
    import { onMount } from 'svelte';
    import { Terminal } from '@xterm/xterm';
    import { FitAddon } from '@xterm/addon-fit';
    import { WebglAddon } from '@xterm/addon-webgl';
    import { appStore, uiStore } from '$lib/stores';
    import { createTerminal, createProjectTerminal, isTerminalStale, getTask, getProject } from '$lib/api';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { loadAppSetting, saveAppSetting } from '$lib/storage';
    import { createFocusTrap } from '$lib/a11y';
    import { effectiveOpenTerminalKey, effectiveToggleFullscreenKey, fullscreenToggleTitle, formatShortcut, matchShortcut } from '$lib/utils/keyboard';
    import ResizeHandle from './ResizeHandle.svelte';
    import { t } from '$lib/i18n';
    import '@xterm/xterm/css/xterm.css';

    const HEIGHT_KEY = 'kanban:terminal:height';
    const DEFAULT_HEIGHT = 360;
    const MIN_HEIGHT = 180;
    const MAX_HEIGHT = 800;

    /**
     * The terminal-only screen (`?view=terminal`): the shell is the whole page, so the overlay,
     * height handle, close button, focus trap and the shortcut that closes the panel all go.
     */
    let { standalone = false }: { standalone?: boolean } = $props();

    let term: Terminal | null = null;
    let container: HTMLElement | null = null;
    let kbContainer: HTMLElement | null = null;
    let ws: WebSocket | null = null;
    let panelEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let panelHeight = $state(DEFAULT_HEIGHT);
    let modCtrl = $state(false);
    let fullscreen = $state(false);
    const isMobileCtx = useIsMobile();

    function toggleFullscreen() {
        fullscreen = !fullscreen;
    }
    let modAlt = $state(false);
    // Active port mappings for the badge
    let activePorts = $state<import('$lib/types').AssignedPort[]>([]);


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
        return () => { if (kbContainer) kbContainer.removeEventListener('pointerdown', handler, { capture: true }); };
    });

    // Close button tooltip — the shortcut toggles the panel, so it also closes.
    const closeButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key ? t('terminal.closeWithShortcut', { shortcut: formatShortcut(key) }) : t('terminal.close');
    });

    const fullscreenTitle = $derived(fullscreenToggleTitle(appStore.config));

    // Open/close terminal shortcut: while the panel is open, pressing the
    // binding closes it (the global handler in App.svelte opens it). Capture
    // phase so it wins over xterm's own keydown, which would send Ctrl+` as
    // a space to the shell.
    $effect(() => {
        if (standalone) return;
        const handleKeydown = (e: KeyboardEvent) => {
            if (e.defaultPrevented || e.repeat) return;
            const key = effectiveOpenTerminalKey(appStore.config);
            if (!key || !matchShortcut(key, e)) return;
            e.preventDefault();
            e.stopPropagation();
            close();
        };
        document.addEventListener('keydown', handleKeydown, true);
        return () => document.removeEventListener('keydown', handleKeydown, true);
    });

    // Toggle-fullscreen shortcut (default Ctrl+Up): fires only while focus is
    // inside the panel; capture phase so it wins over xterm's own keydown.
    // Focus elsewhere (e.g. chat above the panel) is handled by that area.
    $effect(() => {
        if (standalone) return;
        const handleKeydown = (e: KeyboardEvent) => {
            if (e.defaultPrevented || e.repeat) return;
            const key = effectiveToggleFullscreenKey(appStore.config);
            if (!key || !matchShortcut(key, e)) return;
            const active = document.activeElement;
            if (!(panelEl && active instanceof HTMLElement && panelEl.contains(active))) return;
            e.preventDefault();
            e.stopPropagation();
            toggleFullscreen();
        };
        document.addEventListener('keydown', handleKeydown, true);
        return () => document.removeEventListener('keydown', handleKeydown, true);
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
    $effect(() => {
        if (standalone) return;
        loadHeight().then(h => { panelHeight = h; }).catch(() => {});
    });

    // Create/recreate the terminal WebSocket for the current project/task.
    function createTerminalWS(proj: import('$lib/types').Project | null, task: import('$lib/types').Task | null) {
        if (proj && task) {
            ws = createTerminal(proj.id, task.id, term!, close, transformInput);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else if (proj) {
            ws = createProjectTerminal(proj.id, term!, close, transformInput);
            ws.onerror = (e) => console.error('Terminal WebSocket error:', e);
        } else {
            term?.writeln(t('terminal.noProject'));
        }
    }

    function close() {
        fullscreen = false;
        trap?.destroy();
        trap = null;
        uiStore.showTerminal = false;
        if (ws) ws.close();
        ws = null;
        setTimeout(() => triggerEl?.focus(), 50);
    }

    // Terminal resize handlers
    let resizeStartY = 0;
    let resizeStartHeight = 0;

    function onTerminalResizeStart(_event: MouseEvent | TouchEvent, startY: number) {
        resizeStartY = startY;
        resizeStartHeight = panelHeight;
    }
    function onTerminalResizeMove(pointerY: number) {
        const delta = resizeStartY - pointerY; // dragging up increases height
        panelHeight = Math.max(MIN_HEIGHT, Math.min(MAX_HEIGHT, resizeStartHeight + delta));
    }
    function onTerminalResizeEnd() {
        saveHeight(panelHeight).catch(() => {});
    }

    onMount(() => {
        if (!container) return;
        triggerEl = document.activeElement as HTMLElement;
        if (panelEl && !standalone) {
            trap = createFocusTrap(panelEl, close);
        }

        // Subscribe to task updates for task terminals (assigned_ports changes)
        // Track task port changes via $effect
        $effect(() => {
            const task = appStore.selectedTask;
            if (task && task.assigned_ports && task.assigned_ports.length > 0) {
                activePorts = task.assigned_ports;
            } else if (!task) {
                // Switched to project-level terminal
                activePorts = appStore.projectTerminalPorts;
            } else {
                activePorts = [];
            }
        });

        // Track project-level terminal ports via $effect
        $effect(() => {
            const ports = appStore.projectTerminalPorts;
            if (!appStore.selectedTask) {
                activePorts = ports;
            }
        });

        // Fetch fresh port data from API to avoid stale store values.
        // Stores can be empty/stale when the terminal opens (e.g., container
        // already exists so no WebSocket broadcast is sent).
        (async () => {
            const proj = appStore.selectedProject;
            const task = appStore.selectedTask;
            if (!proj) return;
            if (task) {
                // Task terminal: fetch latest task to get assigned_ports
                try {
                    const fresh = await getTask(proj.id, task.id);
                    if (fresh.assigned_ports && fresh.assigned_ports.length > 0) {
                        activePorts = fresh.assigned_ports;
                    }
                } catch { /* stale data is fine — WebSocket will update */ }
            } else {
                // Project terminal: fetch latest project to get active_terminal_ports
                try {
                    const fresh = await getProject(proj.id);
                    if (fresh.active_terminal_ports && fresh.active_terminal_ports.length > 0) {
                        activePorts = fresh.active_terminal_ports;
                        appStore.projectTerminalPorts = fresh.active_terminal_ports;
                    }
                } catch { /* stale data is fine — WebSocket will update */ }
            }
        })();

        term = new Terminal({
            cursorBlink: true,
            fontSize: 13,
            lineHeight: 1.4,
            fontFamily: '"Noto Sans Mono", ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
            theme: terminalTheme(),
        });

        // Local non-null references for closures (term is Terminal | null at module level).
        const terminal = term!;

        let webglRef: WebglAddon | null = new WebglAddon();
        webglRef.onContextLoss(() => { webglRef = null; });
        terminal.loadAddon(webglRef);
        const fit = new FitAddon();
        terminal.loadAddon(fit);
        terminal.open(container);
        fit.fit();
        terminal.focus();

        // terminal.element is only set AFTER open() — must not access before.
        const element = terminal.element!;

        const proj = appStore.selectedProject;
        const task = appStore.selectedTask;
        createTerminalWS(proj, task);

        // A socket that stopped answering is not revived by the page coming back, so this check rides the
        // shared return signal: `visibilitychange` alone never fires for a window that was only unfocused.
        const returnWatcher = createTabReturn({
            revalidateOnReturn: true,
            onReturn: () => {
                if (!ws || !isTerminalStale(ws)) return;
                terminal.write('\r\n\x1b[2mReconnecting...\x1b[0m\r\n');
                ws.close();
                createTerminalWS(appStore.selectedProject, appStore.selectedTask);
            },
        });

        let fitPending = false;
        const observer = new ResizeObserver(() => {
            if (!fitPending) {
                fitPending = true;
                requestAnimationFrame(() => { fit.fit(); fitPending = false; });
            }
        });
        observer.observe(container);

        // Mobile touch selection: long-press → word select → draggable handles + copy/paste.
        let touchStartY = 0;
        let touchVelSamples: { y: number; t: number }[] = [];
        let inertiaRaf: number | null = null;
        let currentTouchY = 0;
        let pixelBufferY = 0;
        let lastTickTime = 0;
        let isDisposed = false;
        let touchStartTime = 0;
        let longPressTimer: ReturnType<typeof setTimeout> | null = null;

        // Selection UI state.
        let selMode = false;
        let selStartRow = 0;
        let selEndRow = 0;
        let selStartCol = 0;
        let selEndCol = 0;
        let draggingHandle: 'start' | 'end' | null = null;
        let docTouchMoveHandler: ((e: TouchEvent) => void) | null = null;
        let docTouchEndHandler: (() => void) | null = null;
        let overlayEl: HTMLElement | null = null;
        let startHandleEl: HTMLElement | null = null;
        let endHandleEl: HTMLElement | null = null;
        let btnBarEl: HTMLElement | null = null;
        let copyBtnEl: HTMLButtonElement | null = null;
        let pasteBtnEl: HTMLButtonElement | null = null;

        const LINE_HEIGHT = 16;
        const LONG_PRESS_MS = 500;

        const stopInertia = () => {
            if (inertiaRaf) { cancelAnimationFrame(inertiaRaf); inertiaRaf = null; }
        };

        const getTermBounds = () => element.getBoundingClientRect();

        /** Measure actual text area offset and cell dimensions. */
        let _cellW: number | null = null;
        let _cellH: number | null = null;
        let _textOffX = 0;
        let _textOffY = 0;

        const measureCells = () => {
            if (_cellW && _cellH) return;
            const termB = element.getBoundingClientRect();
            const canvas = element.querySelector('canvas') as HTMLCanvasElement | null;
            if (canvas) {
                const cB = canvas.getBoundingClientRect();
                _textOffX = cB.left - termB.left;
                _textOffY = cB.top - termB.top;
                _cellW = cB.width / terminal.cols;
                _cellH = cB.height / terminal.rows;
            } else {
                _textOffX = 0;
                _textOffY = 0;
                _cellW = termB.width / terminal.cols;
                _cellH = termB.height / terminal.rows;
            }
        };

        /** Convert page coordinates to viewport row/col. */
        const pageToChar = (pageX: number, pageY: number): { row: number; col: number } => {
            measureCells();
            const b = getTermBounds();
            const relX = pageX - b.left - _textOffX;
            const relY = pageY - b.top - _textOffY;
            return {
                col: Math.max(0, Math.min(terminal.cols - 1, Math.floor(relX / _cellW!))),
                row: Math.max(0, Math.min(terminal.rows - 1, Math.floor(relY / _cellH!))),
            };
        };

        /** Find word boundaries on a buffer row. Returns { start, end } inclusive, or null. */
        const getScrollOffset = (): number => {
            return (terminal.buffer.active as any).ydisp ?? terminal.buffer.active.baseY ?? 0;
        };

        const findWordAt = (viewportRow: number, col: number): { start: number; end: number } | null => {
            const buffer = terminal.buffer.active;
            const absRow = getScrollOffset() + viewportRow;
            const line = buffer.getLine(absRow);
            if (!line) return null;
            const text = line.translateToString(true);
            if (!text.length || col >= text.length) return null;
            if (text[col] === ' ' || !text[col]) return null;
            let s = col;
            while (s > 0 && text[s - 1] !== ' ') s--;
            let e = col;
            while (e < text.length - 1 && text[e + 1] !== ' ') e++;
            return { start: s, end: e };
        };

        const triggerWordSelect = (pageX: number, pageY: number): { startCol: number; endCol: number; row: number } | null => {
            const { row, col } = pageToChar(pageX, pageY);
            const word = findWordAt(row, col);
            if (!word) return null;
            return { startCol: word.start, endCol: word.end, row };
        };

        const applySelection = () => {
            let sr = selStartRow, er = selEndRow, sc = selStartCol, ec = selEndCol;
            if (sr > er || (sr === er && sc > ec)) {
                [sr, er] = [er, sr];
                [sc, ec] = [ec, sc];
            }
            // Skip if no range.
            if (sr === er && sc === ec) {
                positionHandles();
                return;
            }
            // Viewport → absolute rows for term.select().
            const ydisp = getScrollOffset();
            const absStart = ydisp + sr;
            const absEnd = ydisp + er;
            if (absStart === absEnd) {
                const len = ec - sc + 1;
                try { terminal.select(sc, absStart, len); } catch {}
            } else {
                const total = (terminal.cols - sc) + (absEnd - absStart - 1) * terminal.cols + (ec + 1);
                try { terminal.select(sc, absStart, total); } catch {}
            }
            positionHandles();
        };

        const removeSelectionOverlay = () => {
            selMode = false;
            draggingHandle = null;
            try { terminal.clearSelection(); } catch {}
            if (overlayEl) { overlayEl.remove(); overlayEl = null; }
            if (startHandleEl) { startHandleEl.remove(); startHandleEl = null; }
            if (endHandleEl) { endHandleEl.remove(); endHandleEl = null; }
            if (btnBarEl) { btnBarEl.remove(); btnBarEl = null; }
            copyBtnEl = null;
            pasteBtnEl = null;
            if (docTouchMoveHandler) {
                document.removeEventListener('touchmove', docTouchMoveHandler);
                docTouchMoveHandler = null;
            }
            if (docTouchEndHandler) {
                document.removeEventListener('touchend', docTouchEndHandler);
                docTouchEndHandler = null;
            }
        };

        const getRowTop = (row: number): number => {
            measureCells();
            const b = getTermBounds();
            return b.top + _textOffY + row * _cellH!;
        };

        const getColLeft = (col: number): number => {
            measureCells();
            const b = getTermBounds();
            return b.left + _textOffX + col * _cellW!;
        };

        const positionHandles = () => {
            if (!startHandleEl || !endHandleEl) return;
            let sr = selStartRow, er = selEndRow, sc = selStartCol, ec = selEndCol;
            if (sr > er || (sr === er && sc > ec)) {
                [sr, er] = [er, sr];
                [sc, ec] = [ec, sc];
            }
            // Hide handles when no range.
            if (sr === er && sc === ec) {
                startHandleEl.style.display = 'none';
                endHandleEl.style.display = 'none';
                return;
            }
            startHandleEl.style.display = 'block';
            endHandleEl.style.display = 'block';
            const startX = getColLeft(sc);
            const endX = getColLeft(ec + 1);
            const startBottom = getRowTop(sr + 1);
            const endBottom = getRowTop(er + 1);
            const handleW = 28;
            // Clamp to viewport.
            const sLeft = Math.max(0, startX - handleW);
            const eLeft = Math.max(0, Math.min(endX, window.innerWidth - handleW));
            startHandleEl.style.left = `${sLeft}px`;
            startHandleEl.style.top = `${startBottom}px`;
            endHandleEl.style.left = `${eLeft}px`;
            endHandleEl.style.top = `${endBottom}px`;
        };

        const createSelectionOverlay = (x: number, y: number) => {
            selMode = true;
            const word = triggerWordSelect(x, y);
            if (word) {
                selStartRow = word.row;
                selEndRow = word.row;
                selStartCol = word.startCol;
                selEndCol = word.endCol;
            } else {
                // No word: use touch position.
                const { row, col } = pageToChar(x, y);
                selStartRow = row;
                selEndRow = row;
                selStartCol = col;
                selEndCol = col;
            }

            // All four ride the top tier and are appended in the order they should paint in:
            // scrim, handles, then the pill over them.
            overlayEl = document.createElement('div');
            overlayEl.style.cssText = `position:fixed;inset:0;z-index:${Z.max};background:transparent;pointer-events:none;`;

            startHandleEl = document.createElement('div');
            startHandleEl.style.cssText = `position:fixed;z-index:${Z.max};width:28px;height:28px;cursor:grab;touch-action:none;filter:drop-shadow(var(--shadow-raised));`;
            const sSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            sSvg.setAttribute('width', '28');
            sSvg.setAttribute('height', '28');
            sSvg.setAttribute('viewBox', '0 0 28 28');
            sSvg.innerHTML = '<path d="M28,0 L14,0 A14,14 0 0,0 14,28 A14,14 0 0,0 28,14 L28,0 Z" style="fill: var(--color-accent)"/>';
            startHandleEl.appendChild(sSvg);

            endHandleEl = document.createElement('div');
            endHandleEl.style.cssText = startHandleEl.style.cssText;
            const eSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            eSvg.setAttribute('width', '28');
            eSvg.setAttribute('height', '28');
            eSvg.setAttribute('viewBox', '0 0 28 28');
            eSvg.innerHTML = '<path d="M0,0 L14,0 A14,14 0 0,1 14,28 A14,14 0 0,1 0,14 L0,0 Z" style="fill: var(--color-accent)"/>';
            endHandleEl.appendChild(eSvg);

            // Pill container.
            btnBarEl = document.createElement('div');
            btnBarEl.style.cssText = `position:fixed;z-index:${Z.max};display:flex;align-items:center;background:var(--color-accent);border-radius:var(--radius-pill);box-shadow:var(--shadow-popover);overflow:hidden;`;

            copyBtnEl = document.createElement('button');
            copyBtnEl.textContent = t('terminal.copy');
            copyBtnEl.style.cssText = 'padding:7px 16px;border:none;background:transparent;color:var(--color-btn-text);font-size:var(--text-base);font-weight:600;font-family:system-ui,sans-serif;cursor:pointer;touch-action:manipulation;-webkit-tap-highlight-color:transparent;';

            const divider = document.createElement('div');
            divider.style.cssText = 'width:1px;height:20px;background:var(--color-border-bright);';

            pasteBtnEl = document.createElement('button');
            pasteBtnEl.textContent = t('terminal.paste');
            pasteBtnEl.style.cssText = 'padding:7px 16px;border:none;background:transparent;color:var(--color-btn-text);font-size:var(--text-base);font-weight:600;font-family:system-ui,sans-serif;cursor:pointer;touch-action:manipulation;-webkit-tap-highlight-color:transparent;';
            pasteBtnEl.addEventListener('mousedown', (e) => {
                e.preventDefault();
                e.stopPropagation();
                // Hidden textarea + execCommand.
                const ta = document.createElement('textarea');
                ta.style.position = 'fixed';
                ta.style.left = '-9999px';
                document.body.appendChild(ta);
                ta.focus();
                try {
                    document.execCommand('paste');
                    // Read after async execCommand.
                    setTimeout(() => {
                        const text = ta.value;
                        ta.remove();
                        try { terminal.clearSelection(); } catch {}
                        if (text) {
                            terminal.focus();
                            terminal.paste(text);
                        }
                        removeSelectionOverlay();
                    }, 50);
                } catch {
                    ta.remove();
                    terminal.focus();
                    removeSelectionOverlay();
                }
            });
            const doCopy = () => {
                const text = terminal.getSelection();
                if (!text) { removeSelectionOverlay(); return; }
                // execCommand for mobile.
                const ta = document.createElement('textarea');
                ta.value = text;
                ta.style.position = 'fixed';
                ta.style.left = '-9999px';
                document.body.appendChild(ta);
                ta.select();
                try { document.execCommand('copy'); } catch {}
                ta.remove();
                removeSelectionOverlay();
            };
            copyBtnEl.addEventListener('mousedown', (e) => { e.preventDefault(); e.stopPropagation(); doCopy(); });

            document.body.appendChild(overlayEl);
            document.body.appendChild(startHandleEl);
            document.body.appendChild(endHandleEl);

            // Assemble pill.
            const hasSelection = !(selStartRow === selEndRow && selStartCol === selEndCol);
            if (hasSelection) {
                btnBarEl.appendChild(copyBtnEl);
                btnBarEl.appendChild(divider);
            }
            btnBarEl.appendChild(pasteBtnEl);
            document.body.appendChild(btnBarEl);

            applySelection();
            positionBtnBar(btnBarEl);
            setupDragListeners();
        };

        const positionBtnBar = (bar: HTMLElement) => {
            let sr = selStartRow, er = selEndRow, sc = selStartCol, ec = selEndCol;
            if (sr > er || (sr === er && sc > ec)) {
                [sr, er] = [er, sr];
                [sc, ec] = [ec, sc];
            }
            const hasSelection = !(sr === er && sc === ec);
            let centerX: number;
            if (hasSelection) {
                centerX = (getColLeft(sc) + getColLeft(ec + 1)) / 2;
            } else {
                centerX = getColLeft(sc) + (_cellW || 7) / 2;
            }
            const selTop = getRowTop(sr);
            const barW = bar.offsetWidth || 120;
            let left = centerX - barW / 2;
            left = Math.max(4, Math.min(left, window.innerWidth - barW - 4));
            bar.style.left = `${left}px`;
            bar.style.top = `${selTop - 36}px`;
        };

        /** Drag the grabbed corner under the finger, two rows up so the selection stays visible. */
        const dragHandleTo = (touch: Touch) => {
            const { row, col } = pageToChar(touch.pageX, touch.pageY);
            const offsetRow = Math.max(0, row - 2);
            if (draggingHandle === 'start') {
                selStartRow = offsetRow;
                selStartCol = col;
            } else {
                selEndRow = offsetRow;
                selEndCol = col;
            }
            applySelection();
            positionHandles();
            if (btnBarEl) positionBtnBar(btnBarEl);
        };

        const setupDragListeners = () => {
            const handleTouchStart = (which: 'start' | 'end') => (e: TouchEvent) => {
                e.preventDefault();
                e.stopPropagation();
                draggingHandle = which;
            };
            if (startHandleEl) startHandleEl.addEventListener('touchstart', handleTouchStart('start'));
            if (endHandleEl) endHandleEl.addEventListener('touchstart', handleTouchStart('end'));

            docTouchMoveHandler = (e: TouchEvent) => {
                if (!draggingHandle) return;
                e.preventDefault();
                dragHandleTo(e.touches[0]);
            };
            docTouchEndHandler = () => {
                if (draggingHandle) draggingHandle = null;
            };
            document.addEventListener('touchmove', docTouchMoveHandler, { passive: false });
            document.addEventListener('touchend', docTouchEndHandler);
        };

        const onTouchStart = (e: TouchEvent) => {
            if (isDisposed) return;
            stopInertia();
            if (selMode) {
                // Tap terminal to dismiss.
                if (!draggingHandle) removeSelectionOverlay();
                return;
            }

            const t = e.touches[0];
            touchStartY = t.pageY;
            currentTouchY = touchStartY;
            pixelBufferY = 0;
            touchStartTime = performance.now();
            touchVelSamples = [{ y: touchStartY, t: touchStartTime }];

            longPressTimer = setTimeout(() => {
                longPressTimer = null;
                createSelectionOverlay(t.pageX, t.pageY);
                if (navigator.vibrate) navigator.vibrate(30);
            }, LONG_PRESS_MS);
        };

        const onTouchMove = (e: TouchEvent) => {
            if (isDisposed) return;

            if (selMode && draggingHandle) {
                e.preventDefault();
                dragHandleTo(e.touches[0]);
                return;
            }

            if (selMode) return;

            if (longPressTimer) {
                const t = e.touches[0];
                if (Math.abs(t.pageY - touchStartY) > 30) {
                    clearTimeout(longPressTimer);
                    longPressTimer = null;
                }
            }

            const now = performance.now();
            currentTouchY = e.touches[0].pageY;
            touchVelSamples.push({ y: currentTouchY, t: now });
            while (touchVelSamples.length > 1 && touchVelSamples[0].t < now - 100) {
                touchVelSamples.shift();
            }
            const deltaY = touchStartY - currentTouchY;
            touchStartY = currentTouchY;
            pixelBufferY += deltaY;
            if (Math.abs(pixelBufferY) >= LINE_HEIGHT) {
                const lines = Math.trunc(pixelBufferY / LINE_HEIGHT);
                pixelBufferY -= lines * LINE_HEIGHT;
                try { terminal.scrollLines(lines); } catch {}
            }
        };

        const onTouchEnd = () => {
            if (isDisposed) return;
            if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
            if (selMode) { draggingHandle = null; return; }

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
                const dt = now - lastTickTime;
                lastTickTime = now;
                remaining *= Math.pow(0.93, dt / 16.67);
                if (Math.abs(remaining) < 0.1) return;
                pixelBufferY += remaining;
                if (Math.abs(pixelBufferY) >= LINE_HEIGHT) {
                    const lines = Math.trunc(pixelBufferY / LINE_HEIGHT);
                    pixelBufferY -= lines * LINE_HEIGHT;
                    try { terminal.scrollLines(lines); } catch {}
                }
                inertiaRaf = requestAnimationFrame(tick);
            };
            inertiaRaf = requestAnimationFrame(tick);
        };

        element.addEventListener('touchstart', onTouchStart, { passive: true });
        element.addEventListener('touchmove', onTouchMove, { passive: true });
        element.addEventListener('touchend', onTouchEnd, { passive: true });

        // Handle paste: send clipboard text to terminal.
        const handlePaste = (e: ClipboardEvent) => {
            const text = e.clipboardData?.getData('text');
            if (text) {
                e.preventDefault();
                e.stopImmediatePropagation();
                terminal.paste(text);
            }
        };
        element.addEventListener('paste', handlePaste, { capture: true });

        function handleKeyDown(e: KeyboardEvent) {
            if (e.key === 'Escape') close();
        }
        window.addEventListener('keydown', handleKeyDown);

        return () => {
            isDisposed = true;
            fitPending = false;
            stopInertia();
            removeSelectionOverlay();
            if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
            if (ws) ws.close();
            ws = null;
            observer.disconnect();
            element.removeEventListener('paste', handlePaste, { capture: true });
            window.removeEventListener('keydown', handleKeyDown);
            returnWatcher.destroy();
            webglRef = null;

            try { terminal.dispose(); } catch {}
            term = null;
        };
    });
</script>

{#if !standalone}
<button type="button" class="terminal-overlay" onclick={close} aria-label={t('terminal.close')} title={t('terminal.close')}></button>
{/if}
<div class="terminal-panel" class:fullscreen={fullscreen || standalone} bind:this={panelEl} role={standalone ? undefined : 'dialog'} aria-modal={standalone ? undefined : 'true'} aria-label={t('terminal.title')} style:height={fullscreen || standalone ? undefined : panelHeight + 'px'}>
    <div class="terminal-header">
        {#if !standalone}
        <ResizeHandle
            axis="horizontal"
            ariaLabel="Resize terminal"
            onDragStart={onTerminalResizeStart}
            onDragMove={onTerminalResizeMove}
            onDragEnd={onTerminalResizeEnd}
        />
        {/if}
        <div class="header-left">
            <span class="title">{t('terminal.title')}</span>
            {#if activePorts.length > 0}
            <span class="port-badge" title={t('terminal.activePortMappings')}>
                <svg width="14" height="14" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                    <path d="M4 7H16M4 13H16" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    <path d="M12 4.5L16 7L12 9.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M8 10.5L4 13L8 15.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                {#each activePorts as port}
                    <span class="port-text">{port.host_port}<span class="port-container">:{port.container_port}</span></span>
                {/each}
            </span>
            {/if}
        </div>
        {#if !standalone}
        <div class="header-actions">
            {#if !isMobileCtx.isMobile}
            <button type="button" class="icon-btn terminal-fullscreen-toggle" onclick={toggleFullscreen} title={fullscreenTitle}>
                {#if fullscreen}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                {/if}
            </button>
            {/if}
            <button class="close-btn" onclick={close} aria-label={t('terminal.close')} title={closeButtonTitle}>✕</button>
        </div>
        {/if}
    </div>
    <div class="terminal-container" bind:this={container}></div>
    <div class="mobile-keyboard" bind:this={kbContainer}>
        <div class="kb-row">
            <button type="button" class="kb-btn" onclick={() => handleKey('Escape')}>esc</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('/')}>/</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('-')}>-</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('Home')}>home</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('ArrowUp')} aria-label={t('terminal.arrowUpAria')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 4l4 5H4l4-5z" fill="currentColor"/></svg>
            </button>
            <button type="button" class="kb-btn" onclick={() => handleKey('End')}>end</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('PageUp')}>pgup</button>
        </div>
        <div class="kb-row">
            <button type="button" class="kb-btn" onclick={() => handleKey('Tab')}>tab</button>
            <button type="button" class="kb-btn kb-mod" class:kb-mod-active={!!modCtrl} onclick={() => toggleModifier('ctrl')}>ctrl</button>
            <button type="button" class="kb-btn kb-mod" class:kb-mod-active={!!modAlt} onclick={() => toggleModifier('alt')}>alt</button>
            <button type="button" class="kb-btn" onclick={() => handleKey('ArrowLeft')} aria-label={t('terminal.arrowLeftAria')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 8l5-4v8l-5-4z" fill="currentColor"/></svg>
            </button>
            <button type="button" class="kb-btn" onclick={() => handleKey('ArrowDown')} aria-label={t('terminal.arrowDownAria')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 12l-4-5h8l-4 5z" fill="currentColor"/></svg>
            </button>
            <button type="button" class="kb-btn" onclick={() => handleKey('ArrowRight')} aria-label={t('terminal.arrowRightAria')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M12 8l-5 4V4l5 4z" fill="currentColor"/></svg>
            </button>
            <button type="button" class="kb-btn" onclick={() => handleKey('PageDown')}>pgdn</button>
        </div>
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    /* The scrim the panel below it dims: one tier apart, so the panel is never painted under its
       own scrim by whatever else is on screen. */
    .terminal-overlay {
        position: fixed;
        inset: 0;
        background: rgb(from var(--color-backdrop) r g b / 0.3);
        z-index: var(--z-overlay);
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
        z-index: var(--z-modal);
        display: flex;
        flex-direction: column;
    }
    .terminal-panel.fullscreen {
        top: 0;
        bottom: 0;
        left: 0;
        right: 0;
        height: 100dvh !important;
        border-radius: 0;
        border: none;
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
    /* ResizeHandle sits at top edge of header, hit area extends upward */
    .terminal-header :global(.resize-handle) {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        height: 10px;
        margin: 0;
        width: 100%;
    }
    .terminal-header :global(.resize-handle::before) {
        top: 0;
        height: 1px;
        transform: none;
        width: 100%;
        left: 0;
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 8px;
        flex: 1;
        min-width: 0;
    }
    .title {
        font-size: var(--text-base);
        font-weight: 600;
        color: var(--color-text-secondary);
    }
    .port-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px 8px;
        border-radius: var(--radius-pill);
        background: rgb(from var(--color-accent-light) r g b / 0.12);
        border: 1px solid rgb(from var(--color-accent-light) r g b / 0.25);
        color: var(--color-accent-light);
        font-size: var(--text-xs);
        font-family: var(--font-mono, monospace);
        font-weight: 500;
        flex-shrink: 0;
    }
    .port-text + .port-text::before {
        content: '|';
        margin-right: 4px;
        color: var(--color-accent-light);
    }
    .port-container {
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
        padding: 2px 6px;
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
    }
    .icon-btn:hover {
        opacity: 1;
        color: var(--color-text-primary);
        background: var(--color-surface-3);
    }

    .close-btn {
        font-size: var(--text-lg);
        padding: 2px 6px;
    }
    .close-btn:hover {
        color: var(--color-text-primary);
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
            z-index: var(--z-sticky);
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
            font-size: var(--text-xs);
            font-weight: 500;
            text-transform: uppercase;
            letter-spacing: 0.03em;
            cursor: pointer;
            transition: background var(--dur-fast), border-color var(--dur-fast), color var(--dur-fast);
            -webkit-tap-highlight-color: transparent;
        }
        .kb-btn:active {
            background: var(--color-surface-3);
            border-color: var(--color-border-bright);
            color: var(--color-btn-text);
        }
        .kb-mod {
            background: var(--color-surface-4);
        }
        .kb-mod-active {
            background: var(--color-accent-dark) !important;
            border-color: var(--color-accent) !important;
            color: var(--color-btn-text) !important;
        }
        /* Push the terminal viewport up so its bottom doesn't sit behind
           the keyboard panel (~75px tall). flex: 1 + margin-bottom in a
           flex column shrinks the flex child by the margin amount. */
        .terminal-container {
            margin-bottom: 85px;
            padding: 4px;
        }
    }
    :global(.terminal) {
        height: 100%;
    }
    :global(.xterm-text) {
        line-height: 1.4 !important;
    }
    :global(.xterm-viewport) {
        -webkit-overflow-scrolling: touch;
    }
</style>
