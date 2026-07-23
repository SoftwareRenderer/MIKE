<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { getEditorDraft, setEditorDraft, clearEditorDraft, loadEditorHistory, saveEditorHistory, clearEditorHistory } from '$lib/storage';
    import { EditorView } from 'codemirror';
    import { EditorState, Compartment, StateField, StateEffect } from '@codemirror/state';
    import { keymap } from '@codemirror/view';
    import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
    import { bracketMatching, foldGutter, indentOnInput, foldKeymap, defaultHighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
    import { autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
    import { LSPClient, languageServerExtensions } from '@codemirror/lsp-client';
    import { requestLSPChannel, type VirtualTransport } from '$lib/utils/lsp-connection-manager';
    import { isLSPAvailable } from '$lib/utils/lsp-connection-manager';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.FILE_EDITOR);
        uiInjector.refreshSlot(INJECTION_POINTS.FILE_EDITOR, { component: 'FileEditor', filePath, projectId });
    });
    import { highlightSelectionMatches } from '@codemirror/search';
    import { drawSelection, dropCursor, highlightActiveLineGutter, highlightActiveLine, rectangularSelection, crosshairCursor, highlightSpecialChars, hoverTooltip } from '@codemirror/view';
    import { gitGutter, type GitGutterState } from '$lib/utils/git-gutter';
    import { darkTheme, darkSyntax } from '$lib/utils/codemirror-theme';
    import CustomSearchPanel from './CustomSearchPanel.svelte';
    import EditorFooter from './EditorFooter.svelte';
    import { mount, unmount } from 'svelte';
    import { javascript } from '@codemirror/lang-javascript';
    import { python } from '@codemirror/lang-python';
    import { go } from '@codemirror/lang-go';
    import { json } from '@codemirror/lang-json';
    import { markdown } from '@codemirror/lang-markdown';
    import { rust } from '@codemirror/lang-rust';
    import { cpp } from '@codemirror/lang-cpp';
    import { php } from '@codemirror/lang-php';
    import { sql } from '@codemirror/lang-sql';
    import { html } from '@codemirror/lang-html';
    import { css } from '@codemirror/lang-css';
    import { svelte } from '@replit/codemirror-lang-svelte';
    import { goTemplate } from '$lib/gotemplate';
    import { getLanguageTreeFromView } from '$lib/highlighter';

    let { projectId, projectPath, filePath, targetLine, active, onFocused, onSymbolsUpdate, onHasChanges, onClose }: {
        projectId: string;
        projectPath?: string;
        filePath: string;
        targetLine: number;
        active: boolean;
        onFocused: (focused: boolean) => void;
        onSymbolsUpdate: (symbols: Array<{ name: string; kind: string; line: number; indent: number }>) => void;
        onHasChanges: (filePath: string, hasChanges: boolean) => void;
        onClose: () => void;
    } = $props();

    let content = $state('');
    let loading = $state(false);
    let saving = $state(false);
    let error = $state('');
    let fileTooLarge = $state(false);
    let fileNotFound = $state(false);
    let baselineContent = $state('');       // disk content at load time (for gutter delta tracking)
    let gutterMarkers = $state<Map<number, string[]> | null>(null); // pre-computed markers from server
    let savedContent = $state('');          // disk content for unsaved-changes tracking

    // Tracked incrementally to avoid O(n) string compare/split on every edit
    let hasChanges = $state(false);
    let lineCount = $state(0);
    let charCount = $state(0);
    let cursorLine = $state(1);
    let cursorCol = $state(1);

    // Undo/redo history persistence
    // historyBase: the base document content (first snapshot)
    // historyDeltas: ordered list of (from, to, insert) deltas applied sequentially
    let historyBase = $state('');
    let historyDeltas = $state<{ from: number; to: number; insert: string }[]>([]);

    // LSP state
    let lspClient: LSPClient | null = null;
    let lspTransport: VirtualTransport | null = null;
    let lspReady = $state(false);
    let lspConnecting = $state(false); // prevents duplicate connections
    let lspError = $state('');
    let lspLanguage = $state(''); // language name for display (e.g., "Go", "TypeScript")
    let lspServerName = $state(''); // server name from LSP (e.g., "gopls")
    let lspServerVersion = $state(''); // server version from LSP (e.g., "v0.15.0")
    let fileUri = $state('');
    let lspCompartment = $state(new Compartment());
    let lspAvailable = $state(false);
    let lspWorkspace = $state(''); // workspace path the LSP client is connected to

    // Notify parent when unsaved changes state changes
    $effect(() => {
        onHasChanges(filePath, hasChanges);
    });

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let editorEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let searchContainerEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let view: EditorView | null = null;
    // Reactive flag to signal when the editor view is created (for LSP effect dependency)
    let editorReady = $state(false);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let searchVisible = $state(false);
    let searchApp: any = null;
    let editorCreating = $state(false); // prevent race between loadFile() and $effect
    let gitGutterState: GitGutterState | null = null; // mutable ref for git gutter

    function getLanguageSupport(path: string) {
        const ext = path.split('.').pop()?.toLowerCase() || '';
        const base = path.split('/').pop()?.toLowerCase() || '';
        const extMap: Record<string, () => any> = {
            js: javascript, mjs: javascript, cjs: javascript, jsx: javascript,
            ts: javascript, tsx: javascript,
            py: python,
            go: go,
            c: cpp, h: cpp,
            cpp: cpp, cc: cpp, cxx: cpp,
            rs: rust,
            json: json,
            md: markdown,
            php: php,
            sql: sql,
            html: html, htm: html,
            css: css, scss: css, sass: css, less: css,
            svelte: svelte,
            tmpl: goTemplate, gohtml: goTemplate,
        };
        if (base === 'dockerfile' || base === 'makefile' || base === 'cmakeLists.txt') return null;
        const support = extMap[ext];
        return support ? support() : null;
    }

    function extractSymbols() {
        if (!view) return;
        const symbols = getLanguageTreeFromView(view, filePath);
        onSymbolsUpdate(symbols);
    }

    function openSearchPanel() {
        if (!view || !searchContainerEl) return;
        searchVisible = true;
        const editorView = view;
        requestAnimationFrame(() => {
            if (searchContainerEl && !searchApp) {
                searchApp = mount(CustomSearchPanel, {
                    target: searchContainerEl,
                    props: {
                        view: editorView,
                        onClose: () => {
                            searchVisible = false;
                            unmount(searchApp);
                            searchApp = null;
                            editorView?.focus();
                        },
                    },
                });
            }
        });
    }

    function closeSearchPanel() {
        searchVisible = false;
        if (searchApp) {
            unmount(searchApp);
            searchApp = null;
        }
        view?.focus();
    }

    // Custom keymap to replace default search with our panel + save shortcut
    const searchKeymapOverride = keymap.of([
        {
            key: 'Ctrl-f',
            macKey: 'Cmd-f',
            run: () => {
                openSearchPanel();
                return true;
            },
        } as any,
        {
            key: 'Ctrl-h',
            macKey: 'Cmd-h',
            run: () => {
                openSearchPanel();
                return true;
            },
        } as any,
        {
            key: 'Ctrl-s',
            macKey: 'Cmd-s',
            run: () => {
                if (hasChanges && !fileTooLarge) {
                    handleSave();
                }
                return true;
            },
        } as any,
    ]);

    function createEditor() {
        if (!editorEl || editorCreating) return;
        editorCreating = true;
        const langSupport = getLanguageSupport(filePath);
        // Use base content so delta replay starts from the saved state.
        const initialDoc = historyBase || content;
        const gitGutterExt = gitGutter();
        gitGutterState = gitGutterExt.state;
        gitGutterExt.state.initialMarkers = gutterMarkers;
        gitGutterExt.state.baselineContent = baselineContent;
        const extensions = [
            // Explicit setup (replaces basicSetup, excludes searchKeymap)
            gitGutterExt.extension,
            highlightActiveLineGutter(),
            highlightSpecialChars(),
            history(),
            foldGutter({
                markerDOM: (open) => {
                    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
                    svg.setAttribute('width', '12');
                    svg.setAttribute('height', '12');
                    svg.setAttribute('viewBox', '0 0 16 16');
                    svg.setAttribute('fill', 'none');
                    const transform = open ? 'translateY(2px)' : 'translateY(2px) rotate(-90deg)';
                    svg.style.cssText =
                        'cursor:pointer;opacity:0.8;transition:transform 0.15s ease;transform:' + transform;
                    svg.style.transformOrigin = 'center';
                    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
                    path.setAttribute('d', 'M4 6l4 4 4-4');
                    path.setAttribute('stroke', 'currentColor');
                    path.setAttribute('stroke-width', '1.5');
                    path.setAttribute('stroke-linecap', 'round');
                    path.setAttribute('stroke-linejoin', 'round');
                    svg.appendChild(path);
                    return svg as unknown as HTMLElement;
                },
            }),
            drawSelection(),
            dropCursor(),
            EditorView.domEventHandlers({
                keydown: (e, view) => {
                    if (e.key === 'Tab') {
                        e.preventDefault();
                    }
                    return false;
                },
            }),
            EditorState.allowMultipleSelections.of(true),
            indentOnInput(),
            syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
            bracketMatching(),
            closeBrackets(),
            lspCompartment.of(autocompletion()),
            rectangularSelection(),
            crosshairCursor(),
            highlightActiveLine(),
            highlightSelectionMatches({ style: true } as any),
            searchKeymapOverride,
            keymap.of([
                ...closeBracketsKeymap,
                ...defaultKeymap,
                ...historyKeymap,
                ...foldKeymap,
                ...completionKeymap,
            ]),
            darkTheme,
            darkSyntax,
            hoverTooltip(async (view, pos) => {
                if (!lspClient || !fileUri) return null;
                try {
                    const result = await lspClient.request('textDocument/definition', {
                        textDocument: { uri: fileUri },
                        position: {
                            line: view.state.doc.lineAt(pos).number - 1,
                            character: pos - view.state.doc.lineAt(pos).from,
                        },
                    });
                    if (result && (Array.isArray(result) ? result.length > 0 : result !== null)) {
                        const locations = Array.isArray(result) ? result : [result];
                        const firstLoc = locations[0];
                        const uri = firstLoc && 'target' in firstLoc ? firstLoc.target?.uri : firstLoc?.uri;

                        if (!uri || !uri.startsWith('file://')) return null;

                        return {
                            pos,
                            above: true,
                            arrows: true,
                            create: () => {
                                const dom = document.createElement('div');
                                dom.className = 'lsp-def-tooltip';
                                const btn = document.createElement('button');
                                btn.className = 'btn-go-def';
                                btn.textContent = 'Go to Definition';
                                btn.onclick = (e) => {
                                    e.stopPropagation();
                                    handleGoToDefinition(pos);
                                };
                                dom.appendChild(btn);
                                return { dom, pos };
                            },
                        };
                    }
                } catch (e) {
                    console.warn('[lsp] hover definition failed:', e);
                }
                return null;
            }),
            EditorView.updateListener.of((update) => {
                if (update.docChanged) {
                    // Sync content first, then compute hasChanges vs disk
                    content = update.state.doc.toString();
                    hasChanges = content !== savedContent;
                    lineCount = update.state.doc.lines;
                    charCount = update.state.doc.length;
                    // Capture delta for undo/redo history persistence.
                    // Only capture user edits (not programmatic changes during restore).
                    if (!(view as any).__restoring) {
                        const deltas: { from: number; to: number; insert: string }[] = [];
                        update.changes.iterChanges((fromA, toA, fromB, toB, inserted) => {
                            deltas.push({ from: fromA, to: toA, insert: inserted.toString() });
                        });
                        if (deltas.length > 0) {
                            historyDeltas = [...historyDeltas, ...deltas];
                            // Persist history (throttled)
                            clearTimeout((view as any)._historyTimer);
                            (view as any)._historyTimer = setTimeout(() => {
                                saveEditorHistory(filePath, historyBase, historyDeltas).catch(() => {});
                            }, 500);
                        }
                    }
                    // Persist unsaved draft (throttled)
                    clearTimeout((view as any)._draftTimer);
                    (view as any)._draftTimer = setTimeout(() => setEditorDraft(filePath, content), 200);
                }
                if (update.selectionSet) {
                    // Update cursor position
                    const pos = update.state.selection.main.head;
                    const line = update.state.doc.lineAt(pos);
                    cursorLine = line.number;
                    cursorCol = pos - line.from + 1;
                    // Extract symbols on selection change (debounced)
                    clearTimeout((view as any)._symbolTimer);
                    (view as any)._symbolTimer = setTimeout(extractSymbols, 300);
                }
            }),
            EditorState.tabSize.of(4),
            indentUnit.of('    '),
            ...(langSupport ? [langSupport] : []),
        ];

        const state = EditorState.create({ doc: initialDoc, extensions });
        view = new EditorView({ state, parent: editorEl });
        // Signal that the editor is ready (triggers LSP initialization)
        editorReady = true;
        // Expose view on DOM for e2e testing
        (view.dom as any).__view = view;

        // Replay persisted deltas to reach current document state.
        // Each delta is applied as a change dispatch, building up the undo stack.
        if (historyDeltas.length > 0) {
            (view as any).__restoring = true;
            for (const delta of historyDeltas) {
                view.dispatch({
                    changes: { from: delta.from, to: delta.to, insert: delta.insert },
                    userEvent: 'internal.restore',
                });
            }
            (view as any).__restoring = false;
            // Sync content after replay
            content = view.state.doc.toString();
            hasChanges = content !== savedContent;
        }

        // Track focus/blur via DOM events on the editor's dom element
        if (view.dom) {
            view.dom.addEventListener('focus', () => {
                onFocused(true);
            }, true);
            view.dom.addEventListener('blur', () => {
                onFocused(false);
            }, true);
        }

        // Extract symbols after a short delay
        setTimeout(extractSymbols, 500);
        // Jump to target line if set (from diff view click)
        if (targetLine >= 1) {
            requestAnimationFrame(() => jumpToLine(targetLine));
        }
    }

    async function loadFile() {
        if (!filePath) {
            lspClient?.disconnect();
            lspClient = null;
            lspReady = false;
            loading = false;
            content = '';
            baselineContent = '';
            gutterMarkers = null;
            view?.destroy();
            view = null;
            return;
        }
        loading = true;
        error = '';
        fileTooLarge = false;
        fileNotFound = false;

        // Destroy old editor and LSP client
        lspClient?.disconnect();
        lspTransport?.release();
        lspTransport = null;
        lspClient = null;
        lspReady = false;
        lspError = '';
        lspWorkspace = '';
        view?.destroy();
        view = null;
        editorReady = false;
        editorCreating = false;
        content = '';
        baselineContent = '';
        gutterMarkers = null;
        hasChanges = false;
        lineCount = 0;
        charCount = 0;
        cursorLine = 1;
        cursorCol = 1;
        historyBase = '';
        historyDeltas = [];

        try {
            const res = await fetch(`/api/fs/read?path=${encodeURIComponent(filePath)}&project=${encodeURIComponent(projectId)}`);
            if (!res.ok) {
                if (res.status === 413) {
                    fileTooLarge = true;
                    return;
                }
                if (res.status === 404) {
                    fileNotFound = true;
                    return;
                }
                const err = await res.json().catch(() => ({ error: 'Failed to read file' }));
                throw new Error(err.error || 'Failed to read file');
            }
            const data = await res.json();
            const fileContent = data.content || '';

            // Parse gutter markers from server (diff vs HEAD)
            const rawMarkers = data.gutter_markers;
            if (rawMarkers && typeof rawMarkers === 'object') {
                const m = new Map<number, string[]>();
                for (const [ln, types] of Object.entries(rawMarkers)) {
                    if (Array.isArray(types)) m.set(parseInt(ln, 10), types);
                }
                gutterMarkers = m;
            } else {
                gutterMarkers = null;
            }

            // Use unsaved draft if available, otherwise use current file content
            const draft = await getEditorDraft(filePath);
            content = draft !== null ? draft : fileContent;
            baselineContent = fileContent;
            savedContent = fileContent;
            hasChanges = content !== savedContent;
            lineCount = content ? content.split('\n').length : 0;
            charCount = content.length;

            // Load persisted undo/redo history (auto-evicted if older than 72h)
            const savedHistory = await loadEditorHistory(filePath);
            if (savedHistory && Array.isArray(savedHistory.deltas)) {
                historyBase = savedHistory.base;
                historyDeltas = savedHistory.deltas;
            } else {
                // No history or expired — start fresh from current file content
                historyBase = fileContent;
                historyDeltas = [];
            }
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }

        // Create editor after DOM has updated (loading=false renders the editor div)
        if (!error && !fileTooLarge && !fileNotFound) {
            await new Promise(resolve => requestAnimationFrame(resolve));
            if (editorEl && !view) {
                createEditor();
                // Jump to target line after editor is created (e.g., from search result click)
                if (targetLine >= 1) {
                    requestAnimationFrame(() => jumpToLine(targetLine));
                }
            }
        }
    }

    async function handleGoToDefinition(pos: number) {
        if (!lspClient || !view) return;
        try {
            const result = await lspClient.request('textDocument/definition', {
                textDocument: { uri: fileUri },
                position: {
                    line: view.state.doc.lineAt(pos).number - 1,
                    character: pos - view.state.doc.lineAt(pos).from,
                },
            });

            if (result && (Array.isArray(result) ? result.length > 0 : result !== null)) {
                const locations = Array.isArray(result) ? result : [result];
                const loc = locations[0];
                const location = loc && 'target' in loc ? loc.target : loc;
                const uri = location?.uri;
                const range = location?.range;

                if (!uri || !uri.startsWith('file://') || !range) return;

                const targetPath = uri.replace(/^file:\/\//, '');
                const targetLine = range.start.line + 1;

                if (targetPath === filePath) {
                    jumpToLine(targetLine);
                } else {
                    window.dispatchEvent(new CustomEvent('open-file', {
                        detail: { path: targetPath, line: targetLine },
                    }));
                }
            }
        } catch (e) {
            console.warn('[lsp] go to definition failed:', e);
        }
    }

    async function handleSave() {
        if (saving || !hasChanges || fileTooLarge || fileNotFound || !filePath) return;
        saving = true;
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch(`/api/fs/write?project=${encodeURIComponent(projectId)}`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
                },
                body: JSON.stringify({ path: filePath, content }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({ error: 'Failed to save' }));
                throw new Error(err.error || 'Failed to save');
            }
            savedContent = content;
            hasChanges = false;
            // Update git gutter — disk now matches editor.
            // The current merged markers (server + delta) represent the diff vs HEAD
            // for the saved content. Use them as the new server markers.
            if (view && gitGutterState) {
                gitGutterState.baselineContent = content;
                // Convert the current merged map back to a plain Map for server markers
                const newMarkers = new Map<number, string[]>();
                for (const [ln, types] of gitGutterState.map) {
                    newMarkers.set(ln, types as string[]);
                }
                gitGutterState.initialMarkers = newMarkers;
                gutterMarkers = newMarkers;
                baselineContent = content;
            }
            await clearEditorDraft(filePath);
        } catch (e) {
            error = String(e);
        } finally {
            saving = false;
        }
    }

    /** Position cursor on a specific line and scroll it into view. */
    function jumpToLine(line: number) {
        if (!view || line < 1) return;
        const editorView = view;
        const totalLines = editorView.state.doc.lines;
        const lineNum = Math.min(line, totalLines);
        const lineObj = editorView.state.doc.line(lineNum);
        editorView.dispatch({ selection: { anchor: lineObj.from, head: lineObj.from } });
        editorView.focus();
        // Scroll into view — place line ~30% from top for context above
        requestAnimationFrame(() => {
            const scroller = editorEl?.querySelector('.cm-scroller');
            if (!scroller) return;
            const coords = editorView.coordsAtPos(lineObj.from);
            if (!coords) return;
            const targetScroll = coords.top + scroller.scrollTop - scroller.clientHeight * 0.3;
            scroller.scrollTop = Math.max(0, Math.min(targetScroll, scroller.scrollHeight - scroller.clientHeight));
        });
    }

    // Handle jump-to-line events from symbol outline
    function handleJumpToLine(e: Event) {
        const line = (e as CustomEvent).detail.line;
        jumpToLine(line);
    }

    // Handle save requests from the tab bar (only when this editor is active)
    function handleSaveRequest() {
        if (active) {
            handleSave();
        }
    }

    // Jump to targetLine when editor is ready (from diff view click)
    $effect(() => {
        const line = targetLine;
        if (line >= 1 && view && !loading && !fileTooLarge && !fileNotFound && !error && filePath) {
            requestAnimationFrame(() => jumpToLine(line));
        }
    });

    // Re-create editor when file or project changes
    $effect(() => {
        const _projId = projectId; // track project changes too
        if (filePath) {
            loadFile();
        }
    });

    // Create editor after content is loaded and DOM is ready.
    // Tracks baselineContent so it waits for loadFile() to complete.
    $effect(() => {
        const _ = baselineContent; // force dependency on load completion
        if (!loading && !fileTooLarge && !fileNotFound && !view && editorEl && !editorCreating) {
            requestAnimationFrame(() => {
                if (editorEl && !view && !editorCreating) {
                    createEditor();
                }
            });
        }
    });

    // Initialize LSP after the file and editor are ready
    // Track extension manager state so this effect re-runs when extensions are loaded
    $effect(() => {
        const _editorReady = editorReady;
        const _projPath = projectPath;
        const _projId = projectId;
        // Read extension state to track it as a dependency
        const _extCount = extensionManager.state.extensions.length;
        // Check LSP availability here (outside async) so effect re-runs when extensions load
        const _lspAvail = isLSPAvailable(filePath);

        if (!_editorReady || !view || !_projId || !_projPath) return;
        // Re-initialize if workspace changed (e.g., project switch with same file path)
        if (lspClient && lspWorkspace === _projPath) return;

        async function initLSP() {
            // Check if LSP extension is available for this file
            lspAvailable = _lspAvail;
            if (!lspAvailable) return; // fall back to built-in autocompletion

            lspConnecting = true;
            lspReady = false;
            fileUri = `file://${filePath}`;
            try {
                // Promise resolved when backend sends $server/lspReady notification
                let resolveReady: () => void;
                const readyPromise = new Promise<void>(resolve => { resolveReady = resolve; });

                // Request a virtual transport from the shared connection manager.
                // All editors in the same workspace share one WebSocket.
                const virtTransport = await requestLSPChannel({
                    workspace: _projPath!,
                    filePath,
                    onDisconnected: () => {
                        lspReady = false;
                        lspWorkspace = ''; // clear workspace on disconnect
                    },
                    onServerInfo: (name, version) => {
                        lspServerName = name;
                        if (version) {
                            try {
                                const parsed = JSON.parse(version);
                                lspServerVersion = parsed.Main?.Version || parsed.Version || version;
                            } catch {
                                lspServerVersion = version;
                            }
                        } else {
                            lspServerVersion = '';
                        }
                    },
                    onReady: () => {
                        lspReady = true;
                        resolveReady();
                    },
                });
                lspTransport = virtTransport;

                lspClient = new LSPClient({
                    extensions: languageServerExtensions(),
                    rootUri: `file://${_projPath!}`,
                    // Gopls can take 10-20s to initialize on large workspaces
                    timeout: 30000,
                });

                await lspClient.connect(virtTransport).initializing;

                // Wait for backend $server/lspReady notification (keeps spinner spinning).
                // Fallback: if backend doesn't send it within 5s, assume ready (old backend compat).
                await Promise.race([
                    readyPromise,
                    new Promise<void>(resolve => setTimeout(resolve, 5000)),
                ]);
                if (!lspReady) {
                    lspReady = true; // fallback for old backend
                }
                lspWorkspace = _projPath!; // track which workspace we're connected to
                // Derive language name from file extension
                const ext = filePath.split('.').pop()?.toLowerCase() || '';
                const langMap: Record<string, string> = {
                    go: 'Go', ts: 'TypeScript', tsx: 'TypeScript',
                    js: 'JavaScript', jsx: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
                    py: 'Python', pyi: 'Python', rs: 'Rust',
                };
                lspLanguage = langMap[ext] || ext.toUpperCase();

                // Swap compartment: replace autocompletion() with LSP plugin
                if (view && lspClient) {
                    view.dispatch({
                        effects: lspCompartment.reconfigure(lspClient.plugin(fileUri)),
                    });
                }
            } catch (e) {
                lspError = String(e);
                console.warn('[lsp] connection failed:', e);
            } finally {
                lspConnecting = false;
            }
        }

        initLSP();
    });

    // Attempt LSP reconnection when the client is disconnected and the editor is visible.
    // With shared connections, reconnection creates a new virtual transport from the manager.
    $effect(() => {
        const _lspReady = lspReady;
        const _lspConnecting = lspConnecting;
        const _lspClient = lspClient;
        const _view = view;
        const _projPath = projectPath;
        const _lspAvailable = lspAvailable;

        // Only reconnect if we previously had a client but it's now disconnected
        if (!_lspClient || !_view || !_projPath || !_lspAvailable) return;
        if (_lspReady || _lspClient.connected || _lspConnecting) return; // still connected or connecting

        async function reconnectLSP() {
            lspConnecting = true;
            lspReady = false;
            try {
                // Promise resolved when backend sends $server/lspReady notification
                let resolveReady: () => void;
                const readyPromise = new Promise<void>(resolve => { resolveReady = resolve; });

                // Release old transport and get a new one from the shared manager
                lspTransport?.release();
                lspTransport = null;
                _lspClient?.disconnect();

                const virtTransport = await requestLSPChannel({
                    workspace: _projPath!,
                    filePath,
                    onDisconnected: () => {
                        lspReady = false;
                        lspWorkspace = '';
                    },
                    onServerInfo: (name, version) => {
                        lspServerName = name;
                        if (version) {
                            try {
                                const parsed = JSON.parse(version);
                                lspServerVersion = parsed.Main?.Version || parsed.Version || version;
                            } catch {
                                lspServerVersion = version;
                            }
                        } else {
                            lspServerVersion = '';
                        }
                    },
                    onReady: () => {
                        lspReady = true;
                        resolveReady();
                    },
                });
                lspTransport = virtTransport;

                const newClient = new LSPClient({
                    extensions: languageServerExtensions(),
                    rootUri: `file://${_projPath!}`,
                    timeout: 30000,
                });
                lspClient = newClient;

                await newClient.connect(virtTransport).initializing;

                // Wait for backend $server/lspReady notification
                await Promise.race([
                    readyPromise,
                    new Promise<void>(resolve => setTimeout(resolve, 5000)),
                ]);
                if (!lspReady) {
                    lspReady = true; // fallback for old backend
                }
                lspWorkspace = _projPath!;

                // Re-attach plugin
                if (_view && newClient) {
                    _view.dispatch({
                        effects: lspCompartment.reconfigure(newClient.plugin(fileUri)),
                    });
                }
            } catch (e) {
                console.warn('[lsp] reconnection failed:', e);
            } finally {
                lspConnecting = false;
            }
        }

        reconnectLSP();
    });

    onMount(() => {
        window.addEventListener('jump-to-line', handleJumpToLine);
        window.addEventListener('editor-save-request', handleSaveRequest);
    });

    onDestroy(() => {
        if (searchApp) {
            unmount(searchApp);
            searchApp = null;
        }
        lspClient?.disconnect();
        lspTransport?.release();
        lspTransport = null;
        lspClient = null;
        view?.destroy();
        window.removeEventListener('jump-to-line', handleJumpToLine);
        window.removeEventListener('editor-save-request', handleSaveRequest);
    });
</script>

<div class="file-editor" class:has-file={!!filePath} class:active={active}>
    {#if filePath}
        <!-- Editor content area -->
        <div class="editor-content">
            {#if loading}
                <div class="editor-placeholder">Loading file...</div>
            {:else if error}
                <div class="editor-error">{error}</div>
            {:else if fileNotFound}
                <div class="editor-placeholder">
                    <p style="margin:0 0 8px">File not found</p>
                    <p style="margin:0;font-size:0.75rem;color:var(--color-text-tertiary)">{filePath}</p>
                </div>
            {:else if fileTooLarge}
                <div class="editor-placeholder">File too large to edit (max 1MB)</div>
            {:else}
                <div class="editor-wrapper" bind:this={editorEl}></div>
                <div class="search-container" bind:this={searchContainerEl}></div>
            {/if}
        </div>

        <!-- Footer with line/char count -->
        {#if !loading && !error && !fileTooLarge && !fileNotFound}
            <EditorFooter
                {cursorLine}
                {cursorCol}
                {lineCount}
                {charCount}
                onFind={openSearchPanel}
                lspReady={lspReady}
                lspConnecting={lspConnecting}
                {lspError}
                {lspLanguage}
                {lspServerName}
                {lspServerVersion}
            />
        {/if}
    {/if}

</div>

<style>
    .file-editor {
        display: none;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }
    .file-editor.active {
        display: flex;
    }

    .editor-content {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
        position: relative;
    }

    .editor-placeholder {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
    }
    .editor-error {
        padding: 16px;
        color: var(--color-status-red);
        font-size: 0.8125rem;
    }

    .editor-wrapper {
        flex: 1;
        overflow: hidden;
    }

    :global(.lsp-def-tooltip) {
        display: contents;
    }
    :global(.cm-tooltip .cm-tooltip-arrow) {
        display: none;
    }
    :global(.btn-go-def) {
        background: var(--color-accent);
        color: white;
        border: none;
        border-radius: 3px;
        padding: 2px 6px;
        cursor: pointer;
        font-size: 0.7rem;
        font-weight: 600;
        transition: opacity 0.15s;
        margin-bottom: 6px;
    }
    :global(.btn-go-def:hover) {
        opacity: 0.8;
    }

    .search-container {
        position: absolute;
        bottom: 0;
        left: 0;
        right: 0;
        z-index: 100;
        pointer-events: none;
    }
</style>
