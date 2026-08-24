<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { getEditorDraft, setEditorDraft, clearEditorDraft, loadEditorHistory, saveEditorHistory, clearEditorHistory, loadAppSetting, saveAppSetting } from '$lib/storage';
    import { EditorView } from 'codemirror';
    import { EditorState, Compartment, StateField, StateEffect, RangeSet, Range, type Extension } from '@codemirror/state';
    import { keymap } from '@codemirror/view';
    import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
    import { bracketMatching, foldGutter, indentOnInput, foldKeymap, defaultHighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
    import { autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
    import { LSPClient, languageServerExtensions } from '@codemirror/lsp-client';
    import { requestLSPChannel, createLSPNotificationHandlers, type VirtualTransport } from '$lib/utils/lsp-connection-manager';
    import { isLSPAvailable } from '$lib/utils/lsp-connection-manager';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    let extAnchorEl = $state<HTMLDivElement | null>(null);
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.FILE_EDITOR);
        const anchorEl = extAnchorEl;
        if (!anchorEl) return;
        // taskId lets host capabilities (e.g. preview.serve) resolve the task worktree.
        uiInjector.renderSlot(INJECTION_POINTS.FILE_EDITOR, { component: 'FileEditor', filePath, projectId, taskId }, anchorEl);
        return () => uiInjector.teardownSlot(INJECTION_POINTS.FILE_EDITOR, anchorEl);
    });
    import { highlightSelectionMatches } from '@codemirror/search';
    import { drawSelection, dropCursor, highlightActiveLineGutter, highlightActiveLine, rectangularSelection, crosshairCursor, highlightSpecialChars, hoverTooltip, gutterLineClass, GutterMarker, Decoration, ViewPlugin, type ViewUpdate, type DecorationSet } from '@codemirror/view';
    import { gitGutter, type GitGutterState } from '$lib/utils/git-gutter';
    import { darkTheme, darkSyntax, lightTheme, lightSyntax, isLightTheme } from '$lib/utils/codemirror-theme';
    import { stickyScroll, updateHeadersEffect, sectionHeadersField, type SectionHeader } from '$lib/utils/sticky-scroll';
    import { keepViewportParsed } from '$lib/utils/keep-viewport-parsed';
    import { closeHoverOnScroll } from '$lib/utils/close-hover-on-scroll';
    import { requestLSPSymbols, flattenSymbols, type LSPSymbol } from '$lib/utils/lsp-symbols';
    import { resolveGutterClickRange, toProjectRelative } from '$lib/utils/line-selection';
    import CustomSearchPanel from './CustomSearchPanel.svelte';
    import EditorFooter from './EditorFooter.svelte';
    import { mount, unmount } from 'svelte';
    import { appStore, dialogStore } from '$lib/stores';
    import { fullscreenToggleTitle } from '$lib/utils/keyboard';
    import { reconcileExternalChange } from '$lib/utils/editor-merge';
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

    let {
        projectId,
        projectPath,
        taskId,
        filePath,
        targetLine,
        active = true,
        onFocused = () => {},
        onSymbolsUpdate = () => {},
        onHasChanges = () => {},
        onClose,
        // Presentation mode: 'explorer' (tab bar chrome, external) or 'diff'
        // (task-view diff-panel editor with its own header + confirm overlay).
        mode = 'explorer',
        // Diff-mode props
        baseRef,
        workspaceDir,
        displayPath: displayPathProp,
        onToggleFullscreen,
        mobileFullscreen,
        isMobile,
        pendingFsEvent = null,
        onLinesSelected = null,
        initialScrollTop = 0,
        onScrollTop = () => {},
    }: {
        projectId: string;
        projectPath?: string;
        taskId?: string;
        filePath: string;
        targetLine: number;
        active?: boolean;
        pendingFsEvent?: { op: 'create' | 'delete'; path: string } | null;
        onFocused?: (focused: boolean) => void;
        onSymbolsUpdate?: (symbols: LSPSymbol[]) => void;
        onHasChanges?: (filePath: string, hasChanges: boolean) => void;
        onClose: (saved: boolean) => void;
        mode?: 'explorer' | 'diff';
        baseRef?: string;
        workspaceDir?: string;
        displayPath?: string;
        onToggleFullscreen?: () => void;
        mobileFullscreen?: boolean;
        isMobile?: boolean;
        /** Explorer-mode only: invoked with the gutter line selection so the
         *  caller can open a Create Task modal prefilled with the lines. */
        onLinesSelected?: ((sel: { relativePath: string; startLine: number; lines: string[] }) => void) | null;
        /** Restored editor scroll offset (applied after creation unless a
         *  targetLine jump takes priority). */
        initialScrollTop?: number;
        /** Called (throttled) with the scroller's current scrollTop. */
        onScrollTop?: (top: number) => void;
    } = $props();

    const fullscreenTitle = $derived(fullscreenToggleTitle(appStore.config));

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
    let lspMessages = $state<string[]>([]); // LSP server messages for tooltip
    const lspMessageLog = $derived(lspMessages.join('\n'));
    let fileUri = $state('');
    let lspCompartment = $state(new Compartment());
    let wrapCompartment = $state(new Compartment());
    let lspAvailable = $state(false);
    let wordWrap = $state(false);

    // Load saved word wrap preference
    loadAppSetting('editor-word-wrap', false).then((saved) => {
        wordWrap = saved;
    });
    let lspWorkspace = $state(''); // workspace path the LSP client is connected to

    // Diff-mode state: header display path + unsaved-changes confirm dialog.
    let displayPath = $state('');
    let showConfirm = $state(false);

    // Notify parent when unsaved changes state changes
    $effect(() => {
        onHasChanges(filePath, hasChanges);
    });

    // On any fs_event, reconcile THIS editor's own file against disk
    let lastHandledEvent: { op: string; path: string } | null = null;
    $effect(() => {
        const evt = pendingFsEvent;
        if (!evt) return;
        if (!active) return;
        if (loading || !editorReady || fileNotFound || fileTooLarge) return;
        if (lastHandledEvent === evt) return;
        lastHandledEvent = evt;
        void checkExternalChange();
    });

    /**
     * Re-read the file from disk and reconcile it against the current buffer.
     * Non-overlapping external edits are merged in; a conflict keeps the user's
     * buffer and surfaces a toast. Never silently discards unsaved content.
     */
    async function checkExternalChange() {
        try {
            const taskParam = taskId ? `&task=${encodeURIComponent(taskId)}` : '';
            const res = await fetch(
                `/api/fs/read?path=${encodeURIComponent(filePath)}&project=${encodeURIComponent(projectId)}${taskParam}`,
            );
            if (!res.ok) return;
            const data = await res.json();
            const theirs: string = data.content ?? '';
            const result = reconcileExternalChange(savedContent, content, theirs);
            if (result.kind === 'no-op') return;
            if (result.kind === 'conflict') {
                dialogStore.showToast(
                    `"${(filePath.split('/').pop() || filePath)}" was changed externally and overlaps your unsaved edits. Your edits were kept.`,
                    'error',
                );
                return;
            }
            // reload (no unsaved edits) or merged (user edits preserved)
            applyDiskContent(result.content, theirs);
        } catch {
            // Transient read failure — keep the buffer untouched.
        }
    }

    /** Replace the editor buffer with reconciled content and re-baseline to disk. */
    function applyDiskContent(newContent: string, diskContent: string) {
        if (view) {
            view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: newContent } });
        }
        content = newContent;
        savedContent = diskContent;
        baselineContent = diskContent;
        hasChanges = content !== savedContent;
        lineCount = content ? content.split('\n').length : 0;
        charCount = content.length;
        if (mode === 'explorer') {
            // Stale gutter markers (vs the old baseline) no longer apply.
            if (gitGutterState) gitGutterState.baselineContent = diskContent;
            gutterMarkers = null;
        }
        setEditorDraft(filePath, content).catch(() => {});
    }

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
    // svelte-ignore non_reactive_update — focus callback registered by CustomSearchPanel
    let searchFocusFind: (() => void) | null = null;
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

    async function extractSymbols() {
        if (!view) return;
        // Try LSP first, fall back to Lezer tree analysis
        let symbols = await requestLSPSymbols(lspClient, filePath);
        if (!symbols || symbols.length === 0) {
            const visibleKinds = new Set(['function', 'method', 'class', 'struct', 'enum', 'interface', 'type', 'namespace', 'package', 'module', 'file', 'property', 'field', 'constructor', 'constant', 'event', 'heading']);
            symbols = getLanguageTreeFromView(view, filePath).filter(s => visibleKinds.has(s.kind));
        }
        onSymbolsUpdate(symbols);
        // Update sticky scroll headers (flatten for sticky scroll)
        const flat = flattenSymbols(symbols);
        const sorted = flat
            .filter((s) => s.kind === 'function' || s.kind === 'class' || s.kind === 'method' || s.kind === 'struct' || s.kind === 'enum' || s.kind === 'interface')
            .sort((a, b) => a.line - b.line);
        const headers: SectionHeader[] = sorted.map((s, i) => ({
            line: s.line,
            endLine: i + 1 < sorted.length ? sorted[i + 1].line - 1 : s.endLine,
        }));
        view.dispatch({ effects: updateHeadersEffect.of(headers) });
    }

    function openSearchPanel() {
        if (searchApp) {
            // Panel already open (e.g. user pressed Ctrl+F from the code editor) —
            // refocus the find input instead of no-oping.
            searchFocusFind?.();
            return;
        }
        if (!view || !searchContainerEl) return;
        searchVisible = true;
        const editorView = view;
        requestAnimationFrame(() => {
            if (searchContainerEl && !searchApp) {
                searchApp = mount(CustomSearchPanel, {
                    target: searchContainerEl,
                    props: {
                        view: editorView,
                        onFocusFind: (fn) => { searchFocusFind = fn; },
                        onClose: () => {
                            searchVisible = false;
                            unmount(searchApp);
                            searchApp = null;
                            searchFocusFind = null;
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
        searchFocusFind = null;
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

    // ---- Scroll tracking (persistence for the explorer) ----
    let scrollTrackView: EditorView | null = null;
    let scrollReportTimer: ReturnType<typeof setTimeout> | null = null;
    function handleScrollTopEvent() {
        if (!scrollTrackView) return;
        if (scrollReportTimer) clearTimeout(scrollReportTimer);
        scrollReportTimer = setTimeout(() => {
            scrollReportTimer = null;
            if (scrollTrackView) onScrollTop?.(scrollTrackView.scrollDOM.scrollTop);
        }, 200);
    }

    function destroyView() {
        if (scrollTrackView) {
            scrollTrackView.scrollDOM.removeEventListener('scroll', handleScrollTopEvent);
            if (scrollReportTimer) onScrollTop?.(scrollTrackView.scrollDOM.scrollTop);
            scrollTrackView = null;
        }
        if (scrollReportTimer) {
            clearTimeout(scrollReportTimer);
            scrollReportTimer = null;
        }
        view?.destroy();
        view = null;
    }

    function createEditor() {
        if (!editorEl || editorCreating) return;
        editorCreating = true;
        const langSupport = getLanguageSupport(filePath);
        // Use base content so delta replay starts from the saved state.
        const initialDoc = historyBase || content;
        const gitGutterExt = gitGutter();
        gitGutterState = gitGutterExt.state;
        // Explorer mode uses server-precomputed gutter markers; diff mode computes
        // the gutter vs the baseline ref (initialMarkers stays null).
        gitGutterExt.state.initialMarkers = mode === 'diff' ? null : gutterMarkers;
        gitGutterExt.state.baselineContent = baselineContent;
        const extensions = [
            // Explicit setup (replaces basicSetup, excludes searchKeymap)
            stickyScroll(),
            keepViewportParsed(),
            closeHoverOnScroll(),
            sectionHeadersField,
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
            ...(mode === 'explorer' && onLinesSelected ? lineSelectionExtensions() : []),
            searchKeymapOverride,
            keymap.of([
                indentWithTab,
                ...closeBracketsKeymap,
                ...defaultKeymap,
                ...historyKeymap,
                ...foldKeymap,
                ...completionKeymap,
            ]),
            isLightTheme() ? lightTheme : darkTheme,
            isLightTheme() ? lightSyntax : darkSyntax,
            hoverTooltip(async (view, pos) => {
                if (!lspClient || !fileUri) return null;

                // Create DOM immediately — button hidden until LSP confirms a valid definition
                const dom = document.createElement('div');
                dom.className = 'lsp-def-tooltip';
                const loadingEl = document.createElement('span');
                loadingEl.className = 'lsp-def-loading';
                loadingEl.textContent = '…';
                dom.appendChild(loadingEl);
                const btn = document.createElement('button');
                btn.className = 'btn-go-def';
                btn.style.display = 'none';
                btn.textContent = 'Go to Definition';
                dom.appendChild(btn);

                // Fire LSP request in parallel — tooltip is already visible with loading placeholder
                let isValid = false;
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
                        if (uri && uri.startsWith('file://')) {
                            const defPath = uri.replace('file://', '');
                            // Only valid if definition is within the project workspace
                            isValid = lspWorkspaceRoot ? defPath.startsWith(lspWorkspaceRoot) : false;
                        }
                    }
                } catch (e) {
                    console.warn('[lsp] hover definition failed:', e);
                }

                if (!isValid) return null;

                // Reveal the button, hide the loading placeholder
                loadingEl.style.display = 'none';
                btn.style.display = '';
                btn.onclick = (e) => {
                    e.stopPropagation();
                    handleGoToDefinition(pos);
                };

                return {
                    pos,
                    above: true,
                    arrows: true,
                    create: () => ({ dom, pos }),
                };
            }),
            EditorView.updateListener.of((update) => {
                if (update.docChanged) {
                    // Sync content first, then compute hasChanges vs disk
                    content = update.state.doc.toString();
                    hasChanges = content !== savedContent;
                    lineCount = update.state.doc.lines;
                    charCount = update.state.doc.length;
                    // Notify THIS file's file-editor extensions (e.g. a
                    // markdown preview) that the document changed. Scoped to
                    // this editor's anchor so a change in one open file never
                    // renders into another file's preview. Generic — the host
                    // forwards the text without interpreting it.
                    uiInjector.forwardEvent(INJECTION_POINTS.FILE_EDITOR, 'editor.contentChange', { content }, extAnchorEl);
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
            wrapCompartment.of(wordWrap ? EditorView.lineWrapping : []),
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

        // Track scroll position for persistence
        scrollTrackView = view;
        view.scrollDOM.addEventListener('scroll', handleScrollTopEvent, { passive: true });

        // Extract symbols after a short delay
        setTimeout(extractSymbols, 500);
        // Jump to target line if set
        if (targetLine >= 1) {
            requestAnimationFrame(() => jumpToLine(targetLine));
        } else if (initialScrollTop > 0) {
            requestAnimationFrame(() => {
                const scroller = view?.scrollDOM;
                if (!scroller) return;
                scroller.scrollTop = Math.min(initialScrollTop, Math.max(0, scroller.scrollHeight - scroller.clientHeight));
            });
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
            destroyView();
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
        destroyView();
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
        // The file is about to change
        if (view) dispatchLineSel(view, null);

        try {
            const taskParam = taskId ? `&task=${encodeURIComponent(taskId)}` : '';
            const res = await fetch(`/api/fs/read?path=${encodeURIComponent(filePath)}&project=${encodeURIComponent(projectId)}${taskParam}`);
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

            if (mode === 'diff') {
                // Diff mode: header display path + baseline vs baseRef for the gutter.
                displayPath = displayPathProp || filePath.split('/').pop() || filePath;
                content = fileContent;
                baselineContent = '';
                savedContent = fileContent;
                hasChanges = content !== savedContent;
                lineCount = content ? content.split('\n').length : 0;
                charCount = content.length;

                // Fetch baseline for the git gutter so markers match the diff.
                // Uses the diff panel's baseRef; on failure (new file) baseline
                // stays empty — all lines shown as added.
                const refToUse = baseRef || 'HEAD';
                const headParams = new URLSearchParams({ path: filePath, ref: refToUse });
                if (projectId) headParams.set('project', projectId);
                if (taskId) headParams.set('task', taskId);
                try {
                    const headRes = await fetch(`/api/fs/read?${headParams}`);
                    if (headRes.ok) {
                        const headData = await headRes.json();
                        baselineContent = headData.content || '';
                    } else {
                        baselineContent = '';
                    }
                } catch {
                    baselineContent = '';
                }
            } else {
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
            const taskParam = taskId ? `&task=${encodeURIComponent(taskId)}` : '';
            const res = await fetch(`/api/fs/write?project=${encodeURIComponent(projectId)}${taskParam}`, {
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
            if (mode === 'explorer') {
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
            }
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
        uiInjector.forwardEvent(INJECTION_POINTS.FILE_EDITOR, 'editor.jumpToLine', { line }, extAnchorEl);
    }

    // Handle save requests from the tab bar (only when this editor is active)
    function handleSaveRequest() {
        if (active) {
            handleSave();
        }
    }

    // Handle word wrap toggle from footer
    function handleWrapToggle(enabled: boolean) {
        wordWrap = enabled;
        if (view) {
            view.dispatch({
                effects: wrapCompartment.reconfigure(enabled ? EditorView.lineWrapping : []),
            });
        }
        // Persist preference
        saveAppSetting('editor-word-wrap', enabled).catch(() => {});
    }

    // ---- Gutter line selection (explorer mode → create task) ----
    interface GutterLineSel { start: number; end: number; }
    const lineSelEffect = StateEffect.define<GutterLineSel | null>();
    const lineSelField = StateField.define<GutterLineSel | null>({
        create: () => null,
        update(value, tr) {
            for (const e of tr.effects) {
                if (e.is(lineSelEffect)) value = e.value;
            }
            return value;
        },
    });
    class LineSelGutterMarker extends GutterMarker {
        constructor() { super(); this.elementClass = 'cm-lineSelGutter'; }
        eq(other: GutterMarker): boolean { return other instanceof LineSelGutterMarker; }
    }
    const lineSelGutterMarker = new LineSelGutterMarker();

    function highlightedLineRange(view: EditorView): { start: number; end: number } | null {
        const sel = view.state.selection.main;
        const start = view.state.doc.lineAt(Math.min(sel.from, sel.to)).number;
        const end = view.state.doc.lineAt(Math.max(sel.from, sel.to)).number;
        return end > start ? { start, end } : null;
    }

    function dispatchLineSel(target: EditorView, sel: GutterLineSel | null) {
        target.dispatch({ effects: lineSelEffect.of(sel) });
    }

    /** Document line under a mouse event over the gutter. */
    function gutterLineAt(target: EditorView, event: MouseEvent): number | null {
        const el = event.target instanceof Element ? event.target : null;
        const marker = el?.closest?.('[data-line]');
        if (marker instanceof HTMLElement && marker.dataset.line) {
            const n = parseInt(marker.dataset.line, 10);
            if (!isNaN(n) && n >= 1 && n <= target.state.doc.lines) return n;
        }
        // Fallback (gutter padding / empty area): map the event's Y into the
        // content column so line wrapping and scroll offset are handled.
        const content = target.contentDOM.getBoundingClientRect();
        if (content.width <= 0) return null;
        const pos = target.posAtCoords({ x: content.left + 1, y: event.clientY }, false);
        if (pos == null) return null;
        return target.state.doc.lineAt(pos).number;
    }

    function emitLineSelection(target: EditorView, range: { start: number; end: number }) {
        const doc = target.state.doc;
        const lines: string[] = [];
        for (let ln = range.start; ln <= range.end; ln++) lines.push(doc.line(ln).text);
        onLinesSelected?.({
            relativePath: toProjectRelative(filePath, projectPath || ''),
            startLine: range.start,
            lines,
        });
    }

    /** Build the line-selection extensions (field, highlights, mouse handlers).
     *  Only active in explorer mode with an onLinesSelected callback. */
    function lineSelectionExtensions(): Extension[] {
        const lineSelDecorations = ViewPlugin.fromClass(
            class {
                decorations: DecorationSet;
                constructor(view: EditorView) {
                    this.decorations = lineSelDecorationSet(view.state);
                }
                update(u: ViewUpdate) {
                    const changed = u.docChanged
                        || u.state.field(lineSelField, false) !== u.startState.field(lineSelField, false);
                    if (changed) this.decorations = lineSelDecorationSet(u.state);
                }
            },
            { decorations: v => v.decorations },
        );
        return [
            lineSelField,
            gutterLineClass.compute([lineSelField], (state) => {
                const sel = state.field(lineSelField, false);
                if (!sel) return RangeSet.empty;
                const doc = state.doc;
                const start = Math.max(1, Math.min(sel.start, doc.lines));
                const end = Math.max(start, Math.min(sel.end, doc.lines));
                const from = doc.line(start).from;
                const to = end >= doc.lines ? doc.length : doc.line(end + 1).from;
                return RangeSet.of(lineSelGutterMarker.range(from, to));
            }),
            lineSelDecorations,
            // Native listeners on view.dom — CM6's EditorView.domEventHandlers only
            // attaches to contentDOM, which gutter mousedowns never bubble through.
            ViewPlugin.fromClass(
                class {
                    v: EditorView;
                    constructor(view: EditorView) {
                        this.v = view;
                        view.dom.addEventListener('mousedown', this.onMousedown);
                        view.dom.addEventListener('keydown', this.onKeydown);
                    }
                    destroy() {
                        this.v.dom.removeEventListener('mousedown', this.onMousedown);
                        this.v.dom.removeEventListener('keydown', this.onKeydown);
                    }
                    onMousedown = (event: MouseEvent) => {
                        if (event.button !== 0) return;
                        const el = event.target instanceof Element ? event.target : null;
                        if (el?.closest('.cm-gitGutter')) {
                            const line = gutterLineAt(this.v, event);
                            if (line == null) return;
                            // A click inside the editor's multi-line text
                            // selection selects the whole highlighted range;
                            // anything else selects just the clicked line.
                            const range = resolveGutterClickRange(line, highlightedLineRange(this.v));
                            dispatchLineSel(this.v, range);
                            emitLineSelection(this.v, range);
                            event.preventDefault();
                            this.v.focus();
                            return;
                        }
                        // Clicking in the editor text clears an active selection.
                        if (this.v.state.field(lineSelField, false)) dispatchLineSel(this.v, null);
                    };
                    onKeydown = (event: KeyboardEvent) => {
                        if (event.key === 'Escape' && this.v.state.field(lineSelField, false)) {
                            dispatchLineSel(this.v, null);
                            event.preventDefault();
                        }
                    };
                },
            ),
        ];
    }

    function lineSelDecorationSet(state: EditorState): DecorationSet {
        const sel = state.field(lineSelField, false);
        if (!sel) return Decoration.none;
        const doc = state.doc;
        const start = Math.max(1, Math.min(sel.start, doc.lines));
        const end = Math.max(start, Math.min(sel.end, doc.lines));
        const marks: Range<Decoration>[] = [];
        for (let ln = start; ln <= end; ln++) {
            const line = doc.line(ln);
            marks.push(Decoration.line({ class: 'cm-lineSel' }).range(line.from));
        }
        return Decoration.set(marks);
    }

    // ---- Diff-mode chrome: header back button, unsaved-changes confirm, keyboard ----
    function handleClose() {
        onClose(true);
    }

    function requestClose() {
        if (hasChanges) {
            showConfirm = true;
        } else {
            handleClose();
        }
    }

    function confirmClose(discard: boolean) {
        showConfirm = false;
        if (discard) handleClose();
    }

    function handleKeyDown(e: KeyboardEvent) {
        if ((e.ctrlKey || e.metaKey) && e.key === 's') {
            e.preventDefault();
            if (hasChanges && !fileTooLarge || fileNotFound) handleSave();
            return;
        }
        if (e.key === 'Escape') {
            e.stopPropagation();
            if (showConfirm) {
                showConfirm = false;
                return;
            }
            if (mobileFullscreen && onToggleFullscreen) {
                onToggleFullscreen();
                return;
            }
            requestClose();
            return;
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

    // LSP workspace root and file path.
    // Explorer mode: point the language server at the project root. Sandbox
    // container paths (/home/sandbox/<name>/...) are normalized to host project
    // paths — the LSP sandbox mounts projects at /mnt/<name>, not
    // /home/sandbox/<name>, so without this gopls sees URIs that don't match its
    // workspace root and reports "No packages found".
    // Diff mode: point the language server at the task's worktree so the server
    // analyzes the task's actual working files (not the project HEAD).
    let lspWorkspaceRoot = $derived.by(() => {
        if (mode === 'diff' && workspaceDir && projectId && taskId) {
            return `${workspaceDir.replace(/\/+$/, '')}/${projectId}/${taskId}`;
        }
        return projectPath || '';
    });
    let lspFilePath = $derived.by(() => {
        if (mode === 'diff') {
            if (lspWorkspaceRoot && displayPath) return `${lspWorkspaceRoot}/${displayPath}`;
            if (projectPath && displayPath) return `${projectPath}/${displayPath}`;
            return filePath;
        }
        if (!filePath || !projectPath) return filePath;
        const projectName = projectPath.split('/').filter(Boolean).pop();
        const sandboxPrefix = `/home/sandbox/${projectName}/`;
        if (filePath.startsWith(sandboxPrefix)) {
            return projectPath + '/' + filePath.slice(sandboxPrefix.length);
        }
        return filePath.startsWith('/') ? filePath : (projectPath + '/' + filePath);
    });

    // Initialize LSP after the file and editor are ready
    // Track extension manager state so this effect re-runs when extensions are loaded
    $effect(() => {
        const _editorReady = editorReady;
        const _wsRoot = lspWorkspaceRoot;
        const _projId = projectId;
        // Read extension state to track it as a dependency
        const _extCount = extensionManager.state.extensions.length;
        // Check LSP availability here (outside async) so effect re-runs when extensions load
        const _lspAvail = isLSPAvailable(filePath);

        if (!_editorReady || !view || !_projId || !_wsRoot) return;
        // Re-initialize if workspace changed (e.g., project switch with same file path)
        if (lspClient && lspWorkspace === _wsRoot) return;

        async function initLSP() {
            // Check if LSP extension is available for this file
            lspAvailable = _lspAvail;
            if (!lspAvailable) return; // fall back to built-in autocompletion

            lspConnecting = true;
            lspReady = false;
            fileUri = `file://${lspFilePath}`;
            try {
                // Promise resolved when backend sends $server/lspReady notification
                let resolveReady: () => void;
                const readyPromise = new Promise<void>(resolve => { resolveReady = resolve; });

                // Request a virtual transport from the shared connection manager.
                // All editors in the same workspace share one WebSocket.
                const virtTransport = await requestLSPChannel({
                    workspace: _wsRoot!,
                    filePath: lspFilePath,
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
                    rootUri: `file://${_wsRoot!}`,
                    notificationHandlers: createLSPNotificationHandlers((msg) => {
                        lspMessages = [...lspMessages.slice(-19), msg]; // keep last 20
                    }),
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
                lspWorkspace = _wsRoot!; // track which workspace we're connected to
                // Derive language name from file extension
                const ext = filePath.split('.').pop()?.toLowerCase() || '';
                const langMap: Record<string, string> = {
                    go: 'Go', ts: 'TypeScript', tsx: 'TypeScript',
                    js: 'JavaScript', jsx: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
                    py: 'Python', pyi: 'Python', rs: 'Rust',
                };
                lspLanguage = langMap[ext] || ext.toUpperCase();
                // Fallback: some LSP servers (e.g., typescript-language-server) don't
                // include serverInfo in the initialize response. Use language name.
                if (!lspServerName) {
                    lspServerName = lspLanguage;
                }

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
        const _wsRoot = lspWorkspaceRoot;
        const _lspAvailable = lspAvailable;

        // Only reconnect if we previously had a client but it's now disconnected
        if (!_lspClient || !_view || !_wsRoot || !_lspAvailable) return;
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
                    workspace: _wsRoot!,
                    filePath: lspFilePath,
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
                    rootUri: `file://${_wsRoot!}`,
                    notificationHandlers: createLSPNotificationHandlers((msg) => {
                        lspMessages = [...lspMessages.slice(-19), msg]; // keep last 20
                    }),
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
                lspWorkspace = _wsRoot!;

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
        searchFocusFind = null;
        lspClient?.disconnect();
        lspTransport?.release();
        lspTransport = null;
        lspClient = null;
        destroyView();
        window.removeEventListener('jump-to-line', handleJumpToLine);
        window.removeEventListener('editor-save-request', handleSaveRequest);
    });
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions — role="application" makes this an interactive landmark for the diff-mode keyboard handler -->
<div class="file-editor" role="application" tabindex="-1" class:has-file={!!filePath} class:active={mode === 'diff' ? true : active} class:diff={mode === 'diff'} class:line-sel={mode === 'explorer' && !!onLinesSelected} onkeydown={mode === 'diff' ? handleKeyDown : undefined}>
    {#if mode === 'diff'}
        <!-- Diff-mode header -->
        <div class="editor-header">
            <div class="header-left">
                <button class="btn-back" class:unsaved={hasChanges} onclick={requestClose} aria-label="Close editor" title="Close editor">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <circle class="unsaved-circle" cx="8" cy="8" r="6" fill="currentColor"/>
                        <path class="close-x" d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                </button>
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                    <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                </svg>
                <span class="file-path" title={filePath}>{displayPath}</span>
            </div>
            <div class="editor-actions">
                {#if onToggleFullscreen}
                    <button type="button" class="editor-icon-btn editor-fullscreen-toggle" onclick={onToggleFullscreen} title={fullscreenTitle}>
                        {#if mobileFullscreen}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        {/if}
                    </button>
                {/if}
                <button
                    class="btn-save"
                    onclick={handleSave}
                    disabled={!hasChanges || saving || fileTooLarge}
                    title="Save (Ctrl+S)"
                >
                {#if saving}
                    <svg class="save-spinner" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                        <path d="M12 2a10 10 0 0 1 10 10"/>
                    </svg>
                    Saving...
                {:else}
                    Save
                {/if}
                </button>
            </div>
        </div>
    {/if}
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
                <div class="ext-anchor" bind:this={extAnchorEl}></div>
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
                onWrapToggle={handleWrapToggle}
                lspReady={lspReady}
                lspConnecting={lspConnecting}
                {lspError}
                {lspLanguage}
                {lspServerName}
                {lspServerVersion}
                lspMessage={lspMessageLog}
                lspWorkspace={lspWorkspaceRoot}
                lspFile={lspFilePath}
            />
        {/if}
    {/if}

    {#if mode === 'diff' && showConfirm}
        <div class="confirm-overlay" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-desc">
            <div class="confirm-dialog" onclick={(e) => e.stopPropagation()} role="presentation">
                <h3 id="confirm-title">Unsaved Changes</h3>
                <p id="confirm-desc">You have unsaved changes. Discard them?</p>
                <div class="confirm-actions">
                    <button class="btn-confirm-cancel" onclick={() => confirmClose(false)}>Keep Editing</button>
                    <button class="btn-confirm-discard" onclick={() => confirmClose(true)}>Discard &amp; Close</button>
                </div>
            </div>
        </div>
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

    /* Gutter line selection is enabled — the line numbers are a control */
    .line-sel :global(.cm-gutter.cm-gitGutter) {
        cursor: pointer;
    }

    /* Hover: the line number is replaced by a yellow chat bubble
       ("create a task from these lines"). The number is the last span of the
       gutter cell's [data-line] row (git-gutter.ts). */
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement) {
        position: relative;
    }
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement:hover [data-line] > span:last-child) {
        visibility: hidden;
    }
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement:hover::after) {
        content: '';
        position: absolute;
        left: 50%;
        top: 50%;
        width: 14px;
        height: 14px;
        transform: translate(-50%, -50%);
        pointer-events: none;
        background: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%23E8A400' stroke-width='1.5' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M14 10a1.33 1.33 0 0 1-1.33 1.33H4.67l-2.67 2.67V3.33A1.33 1.33 0 0 1 3.33 2h9.34A1.33 1.33 0 0 1 14 3.33z'/%3E%3C/svg%3E") center / contain no-repeat;
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
    :global(.lsp-def-loading) {
        color: var(--color-text-muted);
        font-size: 0.75rem;
        font-style: italic;
        padding: 2px 6px;
        user-select: none;
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

    /* File-editor UI extension anchor. Overlays the editor content area so an
       extension can render a full-bleed view (e.g. a rendered preview) over
       the code editor. Inert by default: pointer-events are only enabled on
       the extension iframe, so the editor stays interactive when the iframe
       is collapsed (height 0). */
    .ext-anchor {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        z-index: 5;
        pointer-events: none;
        overflow: hidden;
    }
    .ext-anchor :global(iframe) {
        pointer-events: auto;
    }

    /* ---- Diff-mode (task-view diff-panel editor) chrome ---- */
    .file-editor.diff {
        display: flex;
        position: relative;
    }
    .file-editor.diff .editor-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 8px;
        flex-shrink: 0;
        background: var(--color-surface-2);
        border-bottom: 1px solid var(--color-border);
        gap: 8px;
    }
    .file-editor.diff .header-left {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        flex: 1;
        color: var(--color-text-secondary);
    }
    .file-editor.diff .header-left svg {
        flex-shrink: 0;
        opacity: 0.6;
    }
    .file-editor.diff .file-path {
        font-size: 0.75rem;
        font-weight: 500;
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .file-editor.diff .btn-back {
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
        position: relative;
    }
    .file-editor.diff .btn-back:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    /* Unsaved indicator — yellow circle, X appears on hover */
    .file-editor.diff .btn-back .unsaved-circle {
        display: none;
    }
    .file-editor.diff .btn-back .close-x {
        display: block;
    }
    .file-editor.diff .btn-back.unsaved .unsaved-circle {
        display: block;
    }
    .file-editor.diff .btn-back.unsaved .close-x {
        display: none;
    }
    .file-editor.diff .btn-back.unsaved {
        color: var(--color-status-yellow, #f0c000);
    }
    .file-editor.diff .btn-back.unsaved:hover .unsaved-circle {
        display: none;
    }
    .file-editor.diff .btn-back.unsaved:hover .close-x {
        display: block;
    }
    .file-editor.diff .btn-back.unsaved:hover {
        color: var(--color-text-primary);
    }
    .file-editor.diff .editor-actions {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }
    .file-editor.diff .editor-icon-btn {
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
    .file-editor.diff .editor-icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .file-editor.diff .editor-fullscreen-toggle {
        display: flex;
    }
    .file-editor.diff .btn-save {
        padding: 4px 12px;
        background: var(--color-accent-dark);
        color: white;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 5px;
        font-family: inherit;
        flex-shrink: 0;
    }
    .file-editor.diff .btn-save:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .file-editor.diff .btn-save:disabled {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        cursor: not-allowed;
    }
    .file-editor.diff .save-spinner {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
    .file-editor.diff .confirm-overlay {
        position: absolute;
        inset: 0;
        background: rgba(0, 0, 0, 0.3);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 10;
        border-radius: 6px;
    }
    .file-editor.diff .confirm-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px;
        width: 320px;
        max-width: 90%;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
    }
    .file-editor.diff .confirm-dialog h3 {
        margin: 0 0 6px;
        font-size: 0.9375rem;
    }
    .file-editor.diff .confirm-dialog p {
        margin: 0 0 16px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }
    .file-editor.diff .confirm-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }
    .file-editor.diff .btn-confirm-cancel {
        padding: 5px 14px;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .file-editor.diff .btn-confirm-cancel:hover {
        background: var(--color-surface-4);
    }
    .file-editor.diff .btn-confirm-discard {
        padding: 5px 14px;
        background: var(--color-status-red-dark);
        color: white;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .file-editor.diff .btn-confirm-discard:hover {
        opacity: 0.9;
    }
</style>
