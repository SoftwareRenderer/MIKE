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
        let dragRefY = 0;
        let dragRefRow = 0;
        let dragRefCol = 0;
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

        const getTermBounds = () => term.element.getBoundingClientRect();

        /** Measure actual text area offset and cell dimensions. */
        let _cellW: number | null = null;
        let _cellH: number | null = null;
        let _textOffX = 0;
        let _textOffY = 0;

        const measureCells = () => {
            if (_cellW && _cellH) return;
            const termB = term.element.getBoundingClientRect();
            const canvas = term.element.querySelector('canvas') as HTMLCanvasElement | null;
            if (canvas) {
                const cB = canvas.getBoundingClientRect();
                _textOffX = cB.left - termB.left;
                _textOffY = cB.top - termB.top;
                _cellW = cB.width / term.cols;
                _cellH = cB.height / term.rows;
            } else {
                _textOffX = 0;
                _textOffY = 0;
                _cellW = termB.width / term.cols;
                _cellH = termB.height / term.rows;
            }
        };

        /** Convert page coordinates to viewport row/col. */
        const pageToChar = (pageX: number, pageY: number): { row: number; col: number } => {
            measureCells();
            const b = getTermBounds();
            const relX = pageX - b.left - _textOffX;
            const relY = pageY - b.top - _textOffY;
            return {
                col: Math.max(0, Math.min(term.cols - 1, Math.floor(relX / _cellW!))),
                row: Math.max(0, Math.min(term.rows - 1, Math.floor(relY / _cellH!))),
            };
        };

        /** Find word boundaries on a buffer row. Returns { start, end } inclusive, or null. */
        const getScrollOffset = (): number => {
            return (term.buffer.active as any).ydisp ?? term.buffer.active.baseY ?? 0;
        };

        const findWordAt = (viewportRow: number, col: number): { start: number; end: number } | null => {
            const buffer = term.buffer.active;
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
                try { term.select(sc, absStart, len); } catch {}
            } else {
                const total = (term.cols - sc) + (absEnd - absStart - 1) * term.cols + (ec + 1);
                try { term.select(sc, absStart, total); } catch {}
            }
            positionHandles();
        };

        const removeSelectionOverlay = () => {
            selMode = false;
            draggingHandle = null;
            try { term.clearSelection(); } catch {}
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

            overlayEl = document.createElement('div');
            overlayEl.style.cssText = 'position:fixed;inset:0;z-index:9997;background:transparent;pointer-events:none;';

            startHandleEl = document.createElement('div');
            startHandleEl.style.cssText = 'position:fixed;z-index:10000;width:28px;height:28px;cursor:grab;touch-action:none;filter:drop-shadow(0 1px 3px rgba(0,0,0,0.3));';
            const sSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            sSvg.setAttribute('width', '28');
            sSvg.setAttribute('height', '28');
            sSvg.setAttribute('viewBox', '0 0 28 28');
            sSvg.innerHTML = '<path d="M28,0 L14,0 A14,14 0 0,0 14,28 A14,14 0 0,0 28,14 L28,0 Z" fill="#0084FF"/>';
            startHandleEl.appendChild(sSvg);

            endHandleEl = document.createElement('div');
            endHandleEl.style.cssText = startHandleEl.style.cssText;
            const eSvg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            eSvg.setAttribute('width', '28');
            eSvg.setAttribute('height', '28');
            eSvg.setAttribute('viewBox', '0 0 28 28');
            eSvg.innerHTML = '<path d="M0,0 L14,0 A14,14 0 0,1 14,28 A14,14 0 0,1 0,14 L0,0 Z" fill="#0084FF"/>';
            endHandleEl.appendChild(eSvg);

            // Pill container.
            btnBarEl = document.createElement('div');
            btnBarEl.style.cssText = 'position:fixed;z-index:10001;display:flex;align-items:center;background:#0084FF;border-radius:18px;box-shadow:0 2px 12px rgba(0,0,0,0.4);overflow:hidden;';

            copyBtnEl = document.createElement('button');
            copyBtnEl.textContent = 'Copy';
            copyBtnEl.style.cssText = 'padding:7px 16px;border:none;background:transparent;color:#fff;font-size:14px;font-weight:600;font-family:system-ui,sans-serif;cursor:pointer;touch-action:manipulation;-webkit-tap-highlight-color:transparent;';

            const divider = document.createElement('div');
            divider.style.cssText = 'width:1px;height:20px;background:rgba(255,255,255,0.3);';

            pasteBtnEl = document.createElement('button');
            pasteBtnEl.textContent = 'Paste';
            pasteBtnEl.style.cssText = 'padding:7px 16px;border:none;background:transparent;color:#fff;font-size:14px;font-weight:600;font-family:system-ui,sans-serif;cursor:pointer;touch-action:manipulation;-webkit-tap-highlight-color:transparent;';
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
                        try { term.clearSelection(); } catch {}
                        if (text) {
                            term.focus();
                            term.paste(text);
                        }
                        removeSelectionOverlay();
                    }, 50);
                } catch {
                    ta.remove();
                    term.focus();
                    removeSelectionOverlay();
                }
            });
            const doCopy = () => {
                const text = term.getSelection();
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

        const setupDragListeners = () => {
            const handleTouchStart = (which: 'start' | 'end') => (e: TouchEvent) => {
                e.preventDefault();
                e.stopPropagation();
                draggingHandle = which;
            };
            startHandleEl.addEventListener('touchstart', handleTouchStart('start'));
            endHandleEl.addEventListener('touchstart', handleTouchStart('end'));

            docTouchMoveHandler = (e: TouchEvent) => {
                if (!draggingHandle) return;
                e.preventDefault();
                const t = e.touches[0];
                const { row, col } = pageToChar(t.pageX, t.pageY);
                // Offset up 2 rows so selection visible above finger.
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
                const t = e.touches[0];
                const { row, col } = pageToChar(t.pageX, t.pageY);
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
                try { term.scrollLines(lines); } catch {}
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
                    try { term.scrollLines(lines); } catch {}
                }
                inertiaRaf = requestAnimationFrame(tick);
            };
            inertiaRaf = requestAnimationFrame(tick);
        };

        term.element.addEventListener('touchstart', onTouchStart, { passive: true });
        term.element.addEventListener('touchmove', onTouchMove, { passive: true });
        term.element.addEventListener('touchend', onTouchEnd, { passive: true });

        // Handle paste: send clipboard text to terminal.
        const handlePaste = (e: ClipboardEvent) => {
            const text = e.clipboardData?.getData('text');
            if (text && term) {
                e.preventDefault();
                term.write(text);
            }
        };
        term.element.addEventListener('paste', handlePaste, { capture: true });

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
            term.element.removeEventListener('paste', handlePaste, { capture: true });
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
    .copy-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 2px 4px;
        display: flex;
        align-items: center;
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
