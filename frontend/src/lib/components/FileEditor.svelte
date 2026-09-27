<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { t } from '$lib/i18n';
    import { baseName, sandboxMountFromPath } from '$lib/utils/path';
    import { readFsFile, writeFsFile, FsReadRefused, isTransientError } from '$lib/api';
    import { getEditorDraft, setEditorDraft, clearEditorDraft, loadEditorHistory, saveEditorHistory, loadAppSetting, saveAppSetting } from '$lib/storage';
    import { EditorView } from 'codemirror';
    import { EditorState, Transaction, Compartment, StateField, StateEffect, RangeSet, Range, type Extension } from '@codemirror/state';
    import { keymap } from '@codemirror/view';
    import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
    import { bracketMatching, foldGutter, indentOnInput, foldKeymap, defaultHighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
    import { autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
    import { LSPClient, serverCompletion, signatureHelp, serverDiagnostics, formatKeymap, renameKeymap, findReferencesKeymap } from '@codemirror/lsp-client';
    import { lspDefinitionActions } from '$lib/utils/lsp-hover-actions';
    import { isInsideWorkspace, relativeToRoot, type DefinitionTarget } from '$lib/utils/lsp-definition';
    import { requestLSPChannel, type LSPTransportOptions, type VirtualTransport } from '$lib/utils/lsp-connection-manager';
    import { lspClientConfig } from '$lib/utils/lsp-client-config';
    import { isLSPAvailable } from '$lib/utils/lsp-connection-manager';
    import { SaveController, EDITOR_SAVE_SETTLED, saveRetryDelayMs } from '$lib/utils/save-state';
    import { errorText } from '$lib/utils/error-text';
    import { connectionStore } from '$lib/stores/connection.svelte';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    import { isExtensionContentSet } from '$lib/extensions/handlers/editor';
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
    import { drawSelection, dropCursor, highlightActiveLineGutter, highlightActiveLine, rectangularSelection, crosshairCursor, highlightSpecialChars, gutterLineClass, GutterMarker, Decoration, ViewPlugin, type ViewUpdate, type DecorationSet } from '@codemirror/view';
    import { gitGutter, type GitGutterState } from '$lib/utils/git-gutter';
    import { darkTheme, darkSyntax, lightTheme, lightSyntax, lspDocsTheme, isLightTheme } from '$lib/utils/codemirror-theme';
    import { stickyScroll, updateHeadersEffect, sectionHeadersField, buildSectionHeaders, type SectionHeader } from '$lib/utils/sticky-scroll';
    import { keepViewportParsed } from '$lib/utils/keep-viewport-parsed';
    import { closeHoverOnScroll } from '$lib/utils/close-hover-on-scroll';
    import { requestLSPSymbols, flattenSymbols, type LSPSymbol } from '$lib/utils/lsp-symbols';
    import { resolveGutterClickRange, toProjectRelative } from '$lib/utils/line-selection';
    import CustomSearchPanel from './CustomSearchPanel.svelte';
    import EditorFooter from './EditorFooter.svelte';
    import Spinner from './Spinner.svelte';
    import { mount, unmount } from 'svelte';
    import { appStore, dialogStore } from '$lib/stores';
    import { fullscreenToggleTitle } from '$lib/utils/keyboard';
    import { reconcileExternalChange, minimalReplace } from '$lib/utils/editor-merge';
    import { fsEventConcernsFile, type FsEvent } from '$lib/utils/fs-event';
    import { getLanguageSupport, resolveFile } from '$lib/utils/editor-language';
    import { fileSuffix } from '$lib/utils/file-suffix';
    import { getOutline } from '$lib/utils/editor-outline';

    let {
        projectId,
        projectPath,
        taskId,
        filePath,
        targetLine,
        reveal,
        active = true,
        onFocused = () => {},
        onSymbolsUpdate = () => {},
        onSymbolsPending = () => {},
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
        closeOnEscape = false,
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
        /** Count of reveal requests for this file (see the reveal effect below). */
        reveal?: number;
        active?: boolean;
        pendingFsEvent?: { op: 'create' | 'delete'; path: string } | null;
        onFocused?: (focused: boolean) => void;
        onSymbolsUpdate?: (symbols: LSPSymbol[]) => void;
        /** Called when a symbol extraction starts, so the outline can hold what it shows. */
        onSymbolsPending?: () => void;
        onHasChanges?: (filePath: string, hasChanges: boolean) => void;
        onClose: (saved: boolean) => void;
        mode?: 'explorer' | 'diff';
        baseRef?: string;
        workspaceDir?: string;
        displayPath?: string;
        onToggleFullscreen?: () => void;
        mobileFullscreen?: boolean;
        closeOnEscape?: boolean;
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
    let loadedPath = $state('');
    let loading = $derived(!!filePath && filePath !== loadedPath);
    let saving = $state(false);
    const saveController = new SaveController();
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

    // Reconcile THIS editor's own file against disk when an fs_event concerns it. An event naming
    // another file earns a delayed reconcile: the watcher reports one path per 500 ms window and
    // drops the rest, so it is not evidence that this file is unchanged.
    const UNRELATED_EVENT_RECONCILE_MS = 2000;
    let lastHandledEvent: FsEvent | null = null;
    let unrelatedEventTimer: ReturnType<typeof setTimeout> | null = null;

    function clearUnrelatedEventTimer() {
        if (!unrelatedEventTimer) return;
        clearTimeout(unrelatedEventTimer);
        unrelatedEventTimer = null;
    }

    $effect(() => {
        const evt = pendingFsEvent;
        if (!evt) return;
        if (!active) return;
        if (loading || !editorReady || fileNotFound || fileTooLarge) return;
        if (lastHandledEvent === evt) return;
        lastHandledEvent = evt;
        clearUnrelatedEventTimer();
        if (fsEventConcernsFile(evt, filePath, { projectId, taskId, rootPath: projectPath })) {
            void checkExternalChange();
            return;
        }
        unrelatedEventTimer = setTimeout(() => {
            unrelatedEventTimer = null;
            if (!active || loading || fileNotFound || fileTooLarge) return;
            void checkExternalChange();
        }, UNRELATED_EVENT_RECONCILE_MS);
    });

    /**
     * Re-read the file from disk and reconcile it against the current buffer.
     * Non-overlapping external edits are merged in; a conflict keeps the user's
     * buffer and surfaces a toast. Never silently discards unsaved content.
     */
    async function checkExternalChange() {
        try {
            const disk = await readFsFile({ path: filePath, project: projectId, task: taskId });
            const theirs: string = disk.content ?? '';
            const result = reconcileExternalChange(savedContent, content, theirs);
            if (result.kind === 'no-op') return;
            if (result.kind === 'conflict') {
                dialogStore.showToast(
                    t('editor.externalConflict', { name: baseName(filePath) || filePath }),
                    'error',
                );
                return;
            }
            // reload (no unsaved edits) or merged (user edits preserved)
            applyDiskContent(result.content, theirs, parseGutterMarkers(disk.gutter_markers));
        } catch {
            // Transient read failure — keep the buffer untouched.
        }
    }

    /** Gutter markers as the server reports them: line number to change types. Null when the
     *  response carries none. */
    function parseGutterMarkers(raw: unknown): Map<number, string[]> | null {
        if (!raw || typeof raw !== 'object') return null;
        const m = new Map<number, string[]>();
        for (const [ln, types] of Object.entries(raw as Record<string, unknown>)) {
            if (Array.isArray(types)) m.set(parseInt(ln, 10), types);
        }
        return m;
    }

    /** Replace the editor buffer with reconciled content and re-baseline to disk. */
    function applyDiskContent(
        newContent: string,
        diskContent: string,
        markers: Map<number, string[]> | null = null,
    ) {
        if (mode === 'explorer') {
            // The markers the server computed against HEAD replace ones keyed to content that no
            // longer exists, and the baseline moves with them. Assign them before the dispatch: the
            // gutter recomputes inside it, and only when the document changes.
            if (markers) {
                if (gitGutterState) gitGutterState.initialMarkers = markers;
                gutterMarkers = markers;
            }
            if (gitGutterState) gitGutterState.baselineContent = diskContent;
        }
        if (view) {
            // Only what changed is dispatched: replacing the whole document rebuilds every visible
            // line and gutter marker.
            const change = minimalReplace(view.state.doc.toString(), newContent);
            if (change.from !== change.to || change.insert) {
                view.dispatch({ changes: change });
            }
            clearTimeout((view as any)._symbolTimer);
            (view as any)._symbolTimer = setTimeout(extractSymbols, 300);
        }
        content = newContent;
        savedContent = diskContent;
        baselineContent = diskContent;
        hasChanges = content !== savedContent;
        // Disk already says what the buffer says, so a waiting write has nothing left to carry.
        if (!hasChanges) cancelSaveRetry();
        lineCount = content ? content.split('\n').length : 0;
        charCount = content.length;
        setEditorDraft(filePath, content).catch(() => {});
    }

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let editorEl: HTMLDivElement | null = null;
    /** The `.file-editor` root — the identity an extension-scoped save request
     * is matched against (see handlers/editor.ts `saveFile`). */
    let rootEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let searchContainerEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let view: EditorView | null = null;
    // Reactive flag to signal when the editor view is created (for LSP effect dependency)
    let editorReady = $state(false);
    // Retry state for LSP symbol extraction. Headers are only re-extracted on
    // load / save / external change, so a single failed request (e.g. gopls
    // still warming up after a cold sandbox start) must retry with backoff —
    // otherwise the sticky bar stays empty until the next file event.
    let lspRetryTimer: ReturnType<typeof setTimeout> | null = null;
    let lspRetryCount = 0;
    function scheduleLspRetry() {
        if (lspRetryTimer || lspRetryCount >= 8) return; // ~60 s of retries, then stop
        lspRetryCount++;
        const delay = Math.min(1500 * 2 ** (lspRetryCount - 1), 10000);
        lspRetryTimer = setTimeout(() => {
            lspRetryTimer = null;
            extractSymbols();
        }, delay);
    }
    function resetLspRetry() {
        if (lspRetryTimer) {
            clearTimeout(lspRetryTimer);
            lspRetryTimer = null;
        }
        lspRetryCount = 0;
    }
    let searchApp: any = null;
    // svelte-ignore non_reactive_update — focus callback registered by CustomSearchPanel
    let searchFocusFind: (() => void) | null = null;
    let editorCreating = $state(false); // prevent race between loadFile() and $effect
    let gitGutterState: GitGutterState | null = null; // mutable ref for git gutter

    async function extractSymbols() {
        if (!view) return;
        // The outline holds on to what it shows until this answers; see `onSymbolsPending`.
        onSymbolsPending();
        // LSP document symbols are the primary source for both the outline panel and the sticky-scroll headers.
        const lspSymbols = await requestLSPSymbols(lspClient, lspFilePath);
        if (lspSymbols !== null) resetLspRetry();
        // Outline panel: LSP symbols, falling back to Lezer tree analysis
        // when LSP is unavailable for this file or a request just failed —
        // the panel must still refresh after edits, and a fresh heuristic
        // beats stale LSP data.
        let symbols = lspSymbols;
        if (!symbols || symbols.length === 0) {
            const visibleKinds = new Set(['function', 'method', 'class', 'struct', 'enum', 'interface', 'type', 'namespace', 'package', 'module', 'file', 'property', 'field', 'constructor', 'constant', 'event', 'heading']);
            symbols = getOutline(filePath, view.state.doc).filter(s => visibleKinds.has(s.kind));
        }
        onSymbolsUpdate(symbols);
        if (lspSymbols === null && lspClient) {
            // LSP is connected but the request failed or timed out
            if (fileUri) {
                try {
                    lspClient.notification('textDocument/didClose', { textDocument: { uri: fileUri } });
                    lspClient.notification('textDocument/didOpen', {
                        textDocument: {
                            uri: fileUri,
                            languageId: resolveFile(filePath).languageId,
                            version: 0,
                            text: view.state.doc.toString(),
                        },
                    });
                } catch {
                    /* ignore — the retry below covers it */
                }
            }
            scheduleLspRetry();
            return;
        }

        const flat = flattenSymbols(lspSymbols ?? []);
        const declarations = flat
            .filter((s) => s.kind === 'function' || s.kind === 'class' || s.kind === 'method' || s.kind === 'struct' || s.kind === 'enum' || s.kind === 'interface')
            .map((s) => ({ line: s.line, endLine: s.endLine }));
        const headers: SectionHeader[] = buildSectionHeaders(declarations);
        // Only dispatch when the headers actually change — avoids churn on cursor moves.
        const prev = (view.state.field(sectionHeadersField, false) || []);
        if (
            prev.length !== headers.length ||
            prev.some((h, i) => h.line !== headers[i].line || h.endLine !== headers[i].endLine || h.depth !== headers[i].depth)
        ) {
            view.dispatch({ effects: updateHeadersEffect.of(headers) });
        }
    }

    function openSearchPanel() {
        if (searchApp) {
            // Panel already open (e.g. user pressed Ctrl+F from the code editor) —
            // refocus the find input instead of no-oping.
            searchFocusFind?.();
            return;
        }
        if (!view || !searchContainerEl) return;
        const editorView = view;
        requestAnimationFrame(() => {
            if (searchContainerEl && !searchApp) {
                searchApp = mount(CustomSearchPanel, {
                    target: searchContainerEl,
                    props: {
                        view: editorView,
                        onFocusFind: (fn) => { searchFocusFind = fn; },
                        onClose: () => {
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
        // Hover tooltip on the gutter rows
        const gutterTitle = mode === 'explorer' && onLinesSelected
            ? (taskId ? t('editor.createSubtaskFromLines') : t('editor.createTaskFromLines'))
            : undefined;
        const gitGutterExt = gitGutter(gutterTitle ? { title: gutterTitle } : undefined);
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
            // Markdown spacing inside LSP tooltips (hover card, completion info,
            // signature help) — the browser's page-sized block margins are far
            // too much whitespace in a 12px card.
            lspDocsTheme,
            // working across an LSP reconnect. The footer already names the server a waiting card
            // should name.
            lspDefinitionActions({ canJump: canJumpTo, onJump: openDefinition, serverLabel: () => lspServerName }),
            EditorView.updateListener.of((update) => {
                if (update.docChanged) {
                    // Sync content first, then compute hasChanges vs disk
                    content = update.state.doc.toString();
                    hasChanges = content !== savedContent;
                    lineCount = update.state.doc.lines;
                    charCount = update.state.doc.length;
                    // Notify file-editor extensions
                    if (!isExtensionContentSet(update.transactions.map(tr => tr.annotation(Transaction.userEvent)))) {
                        uiInjector.forwardEvent(INJECTION_POINTS.FILE_EDITOR, 'editor.contentChange', { content }, extAnchorEl);
                    }
                    // Capture delta for undo/redo history persistence.
                    // Only capture user edits (not programmatic changes during restore).
                    if (!(view as any).__restoring) {
                        const deltas: { from: number; to: number; insert: string }[] = [];
                        update.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
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
                    // Update cursor position (status bar only — symbol
                    // extraction runs on load, save, and external changes)
                    const pos = update.state.selection.main.head;
                    const line = update.state.doc.lineAt(pos);
                    cursorLine = line.number;
                    cursorCol = pos - line.from + 1;
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
        // The load records its own path, so an overlapping load cannot mark this instance loaded.
        const requestPath = filePath;
        resetLspRetry();
        // Close the current document on the LSP server BEFORE dropping the
        // client: the sandboxed server outlives this client, and a document
        // it keeps holding with stale content makes the next didOpen for the
        // same URI get ignored (gopls re-opens nothing), so documentSymbol
        // would keep answering from the old text.
        if (lspClient && fileUri) {
            try {
                lspClient.notification('textDocument/didClose', { textDocument: { uri: fileUri } });
            } catch {
                /* connection may already be gone */
            }
        }
        if (!filePath) {
            lspClient?.disconnect();
            lspClient = null;
            lspReady = false;
            loadedPath = '';
            content = '';
            baselineContent = '';
            gutterMarkers = null;
            destroyView();
            return;
        }
        loadedPath = '';
        error = '';
        fileTooLarge = false;
        fileNotFound = false;
        cancelSaveRetry();

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
            const data = await readFsFile({ path: filePath, project: projectId, task: taskId });
            const fileContent = data.content || '';

            if (mode === 'diff') {
                // Diff mode: header display path + baseline vs baseRef for the gutter.
                displayPath = displayPathProp || baseName(filePath) || filePath;
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
                try {
                    const head = await readFsFile({ path: filePath, project: projectId, task: taskId, ref: refToUse });
                    baselineContent = head.content || '';
                } catch {
                    baselineContent = '';
                }
            } else {
                gutterMarkers = parseGutterMarkers(data.gutter_markers);

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
            // The two refusals that are answers rather than faults: the file is bigger than an editor
            // shows, or it is gone. Anything else is said with the server's own sentence.
            if (e instanceof FsReadRefused && e.status === 413) fileTooLarge = true;
            else if (e instanceof FsReadRefused && e.status === 404) fileNotFound = true;
            else error = errorText(e, t('editor.readFailed'));
        } finally {
            loadedPath = requestPath;
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

    /**
     * Whether a definition target lives inside the tree this editor is open on.
     * A definition outside it (a module cache, another checkout) is not
     * something the explorers can open.
     */
    function canJumpTo(target: DefinitionTarget): boolean {
        return !!lspWorkspaceRoot && isInsideWorkspace(target.path, lspWorkspaceRoot);
    }

    /**
     * The path the explorer that owns this editor opens a file by — the same
     * path space as this editor's own file. A sandboxed project addresses its
     * files by the container mount (`/home/sandbox/<name>/…`) while the language
     * server answers with host paths, so a definition's path has to be
     * re-rooted or the explorer is handed a file it cannot see.
     */
    function explorerPathFor(target: DefinitionTarget): string {
        const relative = relativeToRoot(target.path, lspWorkspaceRoot);
        if (!relative) return target.path;
        // An editor path this helper does not recognize (a diff editor's worktree
        // path, say) is already in its explorer's space.
        const ownRelative = toProjectRelative(filePath || '', projectPath || '');
        if (!ownRelative || ownRelative === filePath || !filePath.endsWith(ownRelative)) return target.path;
        return filePath.slice(0, filePath.length - ownRelative.length) + relative;
    }

    /**
     * The single jump path — the hover card's button and Ctrl/Cmd+Click both
     * land here, with the location the request already resolved (no second
     * round trip, and no second, subtly different reading of the response).
     *
     * Same file → move the caret. Another file → hand it to the explorer that
     * owns this editor: `taskId` marks a task-scoped jump (task explorer,
     * including a jump out of the diff editor) as opposed to a project one, so
     * a diff never opens the project's HEAD copy of a file.
     */
    function openDefinition(target: DefinitionTarget): void {
        if (!view) return;
        // lspFilePath is the host path the language server was given for this
        // buffer, so it is the only comparison that survives sandbox mounts.
        if (target.path === lspFilePath) {
            jumpToLine(target.line);
            view.focus();
            return;
        }
        if (!canJumpTo(target)) {
            dialogStore.showToast(taskId ? t('lsp.definitionOutsideTask') : t('lsp.definitionOutsideProject'), 'error');
            return;
        }
        window.dispatchEvent(new CustomEvent('open-file', {
            detail: { path: explorerPathFor(target), line: target.line, taskId },
        }));
    }

    /**
     * Whether this editor has something to write. `fileNotFound` refuses, though
     * the keydown handler below tries to allow it — left as it behaves today.
     */
    function saveIsNeeded(): boolean {
        return hasChanges && !fileTooLarge && !fileNotFound;
    }

    /**
     * Tell the explorer no save for this file is running, so its Save button comes
     * back now. Never sent while a save is still running.
     */
    function announceSaveSettled(ok: boolean) {
        if (!filePath) return;
        window.dispatchEvent(new CustomEvent(EDITOR_SAVE_SETTLED, { detail: { path: filePath, ok } }));
    }

    /**
     * The wait between attempts at a save that got no answer. A save is not an `offline-queue` action: a body
     * replayed from IndexedDB would overwrite newer content, so the editor holding the buffer holds the wait.
     * `docs/reconnection.md`.
     */
    let saveRetryAttempt = 0;
    let saveRetryTimer: ReturnType<typeof setTimeout> | null = null;

    function clearSaveRetryTimer() {
        if (!saveRetryTimer) return;
        clearTimeout(saveRetryTimer);
        saveRetryTimer = null;
    }

    /** Nothing left to write: it landed, the buffer matches disk again, or this editor is going away. */
    function cancelSaveRetry() {
        clearSaveRetryTimer();
        saveRetryAttempt = 0;
    }

    /** Arm the next attempt. A refusal does not qualify — the server answered that one, and would answer again. */
    function armSaveRetry(e: unknown) {
        clearSaveRetryTimer();
        if (!isTransientError(e)) {
            saveRetryAttempt = 0;
            return;
        }
        const delay = saveRetryDelayMs(saveRetryAttempt++);
        saveRetryTimer = setTimeout(() => {
            saveRetryTimer = null;
            void handleSave();
        }, delay);
    }

    // An armed wait does not sit out its delay once the socket is back. The timer covers a blip that
    // never dropped the socket.
    $effect(() => {
        if (!connectionStore.connected || !saveRetryTimer) return;
        clearSaveRetryTimer();
        void handleSave();
    });

    async function handleSave() {
        if (!filePath) return;
        if (!saveController.begin(saveIsNeeded())) {
            // Refused with nothing running: there is no save to wait for.
            if (!saveController.busy) announceSaveSettled(false);
            return;
        }
        // This attempt sends the current buffer, so the armed wait is no longer needed.
        clearSaveRetryTimer();
        saving = true;
        let ok = false;
        try {
            await writeFsFile({
                project: projectId,
                task: taskId,
                path: filePath,
                content,
                fallback: t('editor.saveFailed'),
            });
            savedContent = content;
            hasChanges = false;
            ok = true;
            cancelSaveRetry();
            // File changed on disk — re-extract outline symbols and
            // sticky-scroll headers (line numbers may have shifted).
            extractSymbols();
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
                // The bytes are on disk; clearing the draft must not hold the button.
                clearEditorDraft(filePath).catch(() => {});
            }
        } catch (e) {
            // A failed save never writes `error`: that pane replaces the editor. A blip is retried and said
            // nowhere; a refusal is toasted once, because waiting will not fix it.
            armSaveRetry(e);
            if (!isTransientError(e)) {
                dialogStore.showToast(errorText(e, t('editor.saveFailed')), 'error');
            }
        } finally {
            saving = false;
            announceSaveSettled(ok);
        }
        // A save asked for during that write runs now if anything is still unsaved, unless a retry is
        // already armed to carry it.
        if (saveController.settle(saveIsNeeded()) && !saveRetryTimer) await handleSave();
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

    // Handle save requests: from the tab bar (window-level, unscoped — only
    // this editor if it is the active one) or from a UI extension mounted in
    // THIS instance (`detail.scope` carries the owning `.file-editor` root, so
    // an extension can only save the file it is attached to).
    function handleSaveRequest(e: Event) {
        const scope = (e as CustomEvent).detail?.scope as HTMLElement | undefined;
        if (scope) {
            if (scope === rootEl) handleSave();
            return;
        }
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
    // hover preview: while the mouse is over a gutter line inside a
    // multi-line editor text selection, that range is previewed in the gutter
    const hoverSelEffect = StateEffect.define<GutterLineSel | null>();
    const hoverSelField = StateField.define<GutterLineSel | null>({
        create: () => null,
        update(value, tr) {
            for (const e of tr.effects) {
                if (e.is(hoverSelEffect)) value = e.value;
            }
            return value;
        },
    });
    class LineSelGutterMarker extends GutterMarker {
        readonly elementClass: string;
        constructor(elementClass: string) {
            super();
            this.elementClass = elementClass;
        }
        eq(other: GutterMarker): boolean {
            return other instanceof LineSelGutterMarker && other.elementClass === this.elementClass;
        }
    }

    const lineSelGutterMarker = new LineSelGutterMarker('cm-lineSelGutter');
    const lineSelInnerGutterMarker = new LineSelGutterMarker('cm-lineSelGutterInner');

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
            hoverSelField,
            gutterLineClass.compute([lineSelField, hoverSelField], (state) => {
                // The hover preview takes precedence over the confirmed selection
                const sel = state.field(hoverSelField, false) ?? state.field(lineSelField, false);
                if (!sel) return RangeSet.empty;
                const doc = state.doc;
                const start = Math.max(1, Math.min(sel.start, doc.lines));
                const end = Math.max(start, Math.min(sel.end, doc.lines));
                // gutterLineClass only collects markers at each line's start position
                const ranges: Range<GutterMarker>[] = [];
                for (let ln = start; ln <= end; ln++) {
                    ranges.push(lineSelGutterMarker.range(doc.line(ln).from));
                    if (ln > start && ln < end) ranges.push(lineSelInnerGutterMarker.range(doc.line(ln).from));
                }
                return RangeSet.of(ranges);
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
                        view.dom.addEventListener('mouseover', this.onMouseOver);
                        view.dom.addEventListener('mouseout', this.onMouseOut);
                    }
                    destroy() {
                        this.v.dom.removeEventListener('mousedown', this.onMousedown);
                        this.v.dom.removeEventListener('keydown', this.onKeydown);
                        this.v.dom.removeEventListener('mouseover', this.onMouseOver);
                        this.v.dom.removeEventListener('mouseout', this.onMouseOut);
                    }
                    /** The preview snapshots the text selection at hover time —
                     *  invalidate it when the selection, doc, or viewport
                     *  changes under a stationary cursor. */
                    update(u: ViewUpdate) {
                        if (
                            (u.docChanged || u.selectionSet || u.viewportChanged) &&
                            this.v.state.field(hoverSelField, false)
                        ) {
                            this.v.dispatch({ effects: hoverSelEffect.of(null) });
                        }
                    }
                    /** Hover preview: a gutter line inside the editor's
                     *  multi-line text selection previews the whole range
                     *  (the same range a click would select); anything else
                     *  clears the preview. */
                    onMouseOver = (event: MouseEvent) => {
                        const el = event.target instanceof Element ? event.target : null;
                        if (!el?.closest('.cm-gitGutter')) return;
                        const line = gutterLineAt(this.v, event);
                        const range = line == null ? null : resolveGutterClickRange(line, highlightedLineRange(this.v));
                        this.dispatchHover(range && range.end > range.start ? range : null);
                    };
                    onMouseOut = (event: MouseEvent) => {
                        const el = event.target instanceof Element ? event.target : null;
                        if (!el?.closest('.cm-gitGutter')) return;
                        const to = event.relatedTarget instanceof Element ? event.relatedTarget : null;
                        if (to?.closest('.cm-gitGutter')) return; // still inside the gutter
                        this.dispatchHover(null);
                    };
                    dispatchHover = (range: GutterLineSel | null) => {
                        const cur = this.v.state.field(hoverSelField, false);
                        if (cur === range || (cur && range && cur.start === range.start && cur.end === range.end)) return;
                        this.v.dispatch({ effects: hoverSelEffect.of(range) });
                    };
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
            if (mobileFullscreen && onToggleFullscreen && !closeOnEscape) {
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
        // Explorer tabs reveal per **request**: `reveal` counts them, so clicking the
        // same search result again scrolls again — a line number on its own cannot
        // say it was asked for twice. The diff editor has no request channel and
        // keeps revealing whenever its line changes.
        const request = reveal;
        if (line >= 1 && (request === undefined || request > 0) && view && !loading && !fileTooLarge && !fileNotFound && !error && filePath) {
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
        const sandboxPrefix = `${sandboxMountFromPath(projectPath)}/`;
        if (filePath.startsWith(sandboxPrefix)) {
            return projectPath + '/' + filePath.slice(sandboxPrefix.length);
        }
        return filePath.startsWith('/') ? filePath : (projectPath + '/' + filePath);
    });

    /**
     * The LSP features this editor uses, listed instead of taking
     * `languageServerExtensions()`. Two reasons:
     *
     *   • that bundle installs its own hover tooltip, and two hover sources on
     *     one editor race to open, resize and close the same tooltip box under
     *     the pointer — the card would shift out from under the click.
     *     `lspDefinitionActions()` is now the only hover.
     *   • its `jumpToDefinitionKeymap` binds F12, which belongs to the browser's
     *     devtools; go-to-definition here is Ctrl/Cmd+Click.
     */
    const lspExtensions = [
        serverCompletion(),
        signatureHelp(),
        serverDiagnostics(),
        keymap.of([...formatKeymap, ...renameKeymap, ...findReferencesKeymap]),
    ];

    /** The callbacks every LSP channel this editor opens answers to; `onReady` is a thunk because
     * the resolver belongs to the connect call. */
    const lspChannelOptions = (workspace: string, filePath: string, onReady: () => void): LSPTransportOptions => ({
        workspace,
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
            onReady();
        },
    });

    // The one config for both clients an editor builds (initial connect and reconnect).
    const lspClientConfigFor = (workspaceRoot: string) =>
        lspClientConfig({
            extensions: lspExtensions,
            rootUri: `file://${workspaceRoot}`,
            // Read when asked: the client outlives the file it connected for.
            getFile: () => filePath,
            onMessage: (msg) => {
                lspMessages = [...lspMessages.slice(-19), msg]; // keep last 20
            },
        });

    // Initialize LSP after the file and editor are ready
    // Track extension manager state so this effect re-runs when extensions are loaded
    $effect(() => {
        const _editorReady = editorReady;
        const _wsRoot = lspWorkspaceRoot;
        const _projId = projectId;
        const _lspConnecting = lspConnecting;
        // Read extension state to track it as a dependency
        const _extCount = extensionManager.state.extensions.length;
        // Check LSP availability here (outside async) so effect re-runs when extensions load
        const _lspAvail = isLSPAvailable(filePath);

        if (!_editorReady || !view || !_projId || !_wsRoot) return;
        // Re-initialize if workspace changed (e.g., project switch with same file path)
        if (lspClient && lspWorkspace === _wsRoot) return;
        if (_lspConnecting) return;

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
                const virtTransport = await requestLSPChannel(lspChannelOptions(_wsRoot!, lspFilePath, () => resolveReady()));
                lspTransport = virtTransport;

                lspClient = new LSPClient(lspClientConfigFor(_wsRoot!));

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
                // Stale-document guard: the sandboxed LSP server persists
                // across sessions. A previous client may have left this URI
                // open with different content (its didClose was lost when its
                // transport dropped), and gopls ignores a didOpen for a URI it
                // still considers open. Close it first so the plugin's
                // didOpen re-opens the document with the current buffer.
                try {
                    lspClient.notification('textDocument/didClose', { textDocument: { uri: fileUri } });
                } catch {
                    /* server may not have the document */
                }
                // Display name only — the protocol id is passed explicitly below. A manifest that
                // declares no label for the file type falls back to its suffix, upper-cased; a file
                // with no suffix has nothing to show, which is the honest answer (and the reason this
                // uses fileSuffix rather than splitting on '.': a dotfile's last segment is its name).
                lspLanguage = resolveFile(filePath).label || fileSuffix(filePath).toUpperCase();
                // Fallback: some LSP servers (e.g., typescript-language-server) don't
                // include serverInfo in the initialize response. Use language name.
                if (!lspServerName) {
                    lspServerName = lspLanguage;
                }

                // Swap compartment: replace autocompletion() with LSP plugin
                if (view && lspClient) {
                    view.dispatch({
                        effects: lspCompartment.reconfigure(lspClient.plugin(fileUri, resolveFile(filePath).languageId)),
                    });
                }
                // LSP just became available — refresh outline symbols and
                // sticky-scroll headers now, instead of waiting for the next
                // selection change or edit to trigger extraction.
                extractSymbols();
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

        if (!_lspClient || !_view || !_wsRoot || !_lspAvailable) return;
        if (_lspReady || _lspClient.connected || _lspConnecting) return;

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

                const virtTransport = await requestLSPChannel(lspChannelOptions(_wsRoot!, lspFilePath, () => resolveReady()));
                lspTransport = virtTransport;

                const newClient = new LSPClient(lspClientConfigFor(_wsRoot!));
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

                // Same stale-document guard as the initial connect.
                try {
                    newClient.notification('textDocument/didClose', { textDocument: { uri: fileUri } });
                } catch {
                    /* server may not have the document */
                }

                // Re-attach plugin
                if (_view && newClient) {
                    _view.dispatch({
                        effects: lspCompartment.reconfigure(newClient.plugin(fileUri, resolveFile(filePath).languageId)),
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
        clearUnrelatedEventTimer();
        cancelSaveRetry();
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
<div class="file-editor" bind:this={rootEl} role="application" tabindex="-1" class:has-file={!!filePath} class:active={mode === 'diff' ? true : active} class:diff={mode === 'diff'} class:line-sel={mode === 'explorer' && !!onLinesSelected} onkeydown={mode === 'diff' ? handleKeyDown : undefined}>
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
                    aria-describedby={fileTooLarge ? 'file-editor-too-large' : undefined}
                >
                {#if saving}
                    <Spinner size={14} decorative />
                    {t('editor.saving')}
                {:else}
                    {t('editor.save')}
                {/if}
                </button>
            </div>
        </div>
    {/if}
    {#if filePath}
        <!-- Editor content area -->
        <div class="editor-content">
            {#if loading}
                <div class="editor-placeholder">{t('editor.loadingFile')}</div>
            {:else if error}
                <div class="editor-error">{error}</div>
            {:else if fileNotFound}
                <div class="editor-placeholder">
                    <p style="margin:0 0 8px">{t('editor.fileNotFound')}</p>
                    <p style="margin:0;font-size:var(--text-xs);color:var(--color-text-tertiary)">{filePath}</p>
                </div>
            {:else if fileTooLarge}
                <div id="file-editor-too-large" class="editor-placeholder">{t('editor.fileTooLarge')}</div>
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
                <h3 id="confirm-title">{t('editor.unsavedTitle')}</h3>
                <p id="confirm-desc">{t('editor.unsavedMessage')}</p>
                <div class="confirm-actions">
                    <button class="btn-confirm-cancel" onclick={() => confirmClose(false)}>{t('editor.keepEditing')}</button>
                    <button class="btn-confirm-discard" onclick={() => confirmClose(true)}>{t('editor.discardAndClose')}</button>
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
       ("create a task from selected lines"). The number is the last span of the
       gutter cell's [data-line] row (git-gutter.ts). */
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement) {
        position: relative;
    }
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement:hover [data-line] > span:last-child) {
        visibility: hidden;
    }
    /* Inner lines of a multi-line selection: the number is replaced by the
       centered vertical connector (styled per-theme in codemirror-theme.ts;
       it yields to the hover bubble via its own :hover rule). */
    .line-sel :global(.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner [data-line] > span:last-child) {
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
        font-size: var(--text-sm);
    }
    .editor-error {
        padding: 16px;
        color: var(--color-status-red);
        font-size: var(--text-sm);
    }

    .editor-wrapper {
        flex: 1;
        overflow: hidden;
    }

    .search-container {
        position: absolute;
        bottom: 0;
        left: 0;
        right: 0;
        z-index: var(--z-dropdown);
        /* Click-through by default: CustomSearchPanel re-enables its own panel, one class deeper and in
           that component's stylesheet. */
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
        z-index: var(--z-content);
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
        font-size: var(--text-xs);
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
        color: var(--color-status-yellow);
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
        border-radius: var(--radius-sm);
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
        font-size: var(--text-xs);
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
    }
    .file-editor.diff .confirm-overlay {
        position: absolute;
        inset: 0;
        background: rgb(from var(--color-backdrop) r g b / 0.3);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: var(--z-sticky);
        border-radius: var(--radius-md);
    }
    .file-editor.diff .confirm-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px;
        width: 320px;
        max-width: 90%;
        box-shadow: var(--shadow-modal);
    }
    .file-editor.diff .confirm-dialog h3 {
        margin: 0 0 6px;
        font-size: var(--text-md);
    }
    .file-editor.diff .confirm-dialog p {
        margin: 0 0 16px;
        font-size: var(--text-sm);
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
        font-size: var(--text-xs);
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
        font-size: var(--text-xs);
        cursor: pointer;
        font-family: inherit;
    }
    .file-editor.diff .btn-confirm-discard:hover {
        opacity: 0.9;
    }
</style>
