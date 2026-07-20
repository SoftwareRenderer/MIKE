<script lang="ts">
    import { onMount, unmount } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { dialogStore } from '$lib/stores';

    // CodeMirror
    import { EditorView } from 'codemirror';
    import { EditorState, Compartment } from '@codemirror/state';
    import { keymap } from '@codemirror/view';
    import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
    import { bracketMatching, foldGutter, indentOnInput, foldKeymap, defaultHighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
    import { autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
    import { LSPClient, languageServerExtensions } from '@codemirror/lsp-client';
    import { requestLSPChannel, type VirtualTransport } from '$lib/utils/lsp-connection-manager';
    import { isLSPEnabled } from '$lib/storage';
    import { highlightSelectionMatches } from '@codemirror/search';
    import { drawSelection, dropCursor, highlightActiveLineGutter, highlightActiveLine, rectangularSelection, crosshairCursor, highlightSpecialChars } from '@codemirror/view';
    import { gitGutter } from '$lib/utils/git-gutter';
    import { darkTheme, darkSyntax } from '$lib/utils/codemirror-theme';
    import CustomSearchPanel from './CustomSearchPanel.svelte';
    import EditorFooter from './EditorFooter.svelte';
    import { mount } from 'svelte';
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

    let { filePath, lineNumber, onClose, displayPath: displayPathProp, projectId, taskId, projectPath, baseRef, onToggleFullscreen, mobileFullscreen, isMobile }: { filePath: string; lineNumber: number; onClose: (saved: boolean) => void; displayPath?: string; projectId?: string; taskId?: string; projectPath?: string; baseRef?: string; onToggleFullscreen?: () => void; mobileFullscreen?: boolean; isMobile?: boolean } = $props();

    let content = $state('');
    let originalContent = $state('');
    let originalContentLoaded = $state(false);
    let savedContent = $state('');
    let loading = $state(true);
    let saving = $state(false);
    let error = $state('');
    let fileTooLarge = $state(false);
    let fileNotFound = $state(false);
    let displayPath = $state('');

    let hasChanges = $derived(content !== savedContent);
    let lineCount = $derived(content.split('\n').length);
    let cursorLine = $state(1);
    let cursorCol = $state(1);

    // svelte-ignore non_reactive_update — DOM ref via bind:this
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let editorEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let searchContainerEl: HTMLDivElement | null = null;
    let showConfirm = $state(false);

    let view: EditorView | null = null;
    let editorReady = $state(false); // signals when editor view is created (for LSP effect)
    let gitGutterState: { original: string } | null = null; // mutable ref for git gutter
    let searchVisible = $state(false);
    let searchApp: any = null;

    // LSP state
    let lspClient: LSPClient | null = null;
    let lspTransport: VirtualTransport | null = null;
    let lspReady = $state(false);
    let lspConnecting = $state(false); // prevents duplicate connections
    let lspError = $state('');
    let fileUri = $state('');
    let lspCompartment = $state(new Compartment());
    let lspEnabled = $state(true);

    function getLanguageSupport(path: string) {
        const ext = path.split('.').pop()?.toLowerCase() || '';
        const base = path.split('/').pop()?.toLowerCase() || '';

        const extMap: Record<string, () => any> = {
            js: javascript, mjs: javascript, cjs: javascript, jsx: javascript, mx: javascript,
            ts: javascript, tsx: javascript,
            py: python, pyw: python,
            go: go,
            c: cpp, h: cpp,
            cpp: cpp, cc: cpp, cxx: cpp, hxx: cpp, hpp: cpp,
            rs: rust,
            json: json,
            md: markdown, mdx: markdown,
            php: php,
            sql: sql,
            html: html, htm: html,
            css: css, scss: css, sass: css, less: css,
            svelte: svelte,
            tmpl: goTemplate, gohtml: goTemplate,
        };

        if (base === 'dockerfile') return null;
        if (base === 'makefile' || base === 'cmakeLists.txt') return null;

        const support = extMap[ext];
        return support ? support() : null;
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

    // Custom keymap to replace default search with our panel
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
    ]);



    function createEditor() {
        if (!editorEl) return;

        const langSupport = getLanguageSupport(filePath);
        const gitGutterExt = gitGutter();
        gitGutterState = gitGutterExt.state;
        gitGutterExt.state.original = originalContent;
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
            EditorView.updateListener.of((update) => {
                if (update.docChanged) {
                    content = update.state.doc.toString();
                }
                if (update.selectionSet) {
                    const pos = update.state.selection.main.head;
                    const line = update.state.doc.lineAt(pos);
                    cursorLine = line.number;
                    cursorCol = pos - line.from + 1;
                }
            }),
            EditorState.tabSize.of(4),
            indentUnit.of('    '),
            ...(langSupport ? [langSupport] : []),
        ];

        const state = EditorState.create({
            doc: content,
            extensions,
        });

        view = new EditorView({
            state,
            parent: editorEl,
        });
        // Signal that the editor is ready (triggers LSP initialization)
        editorReady = true;

        // Scroll to target line, centered in the viewport
        if (lineNumber >= 1) {
            requestAnimationFrame(() => {
                if (!view) return;
                const totalLines = view.state.doc.lines;
                const lineNum = Math.min(lineNumber, totalLines);
                const line = view.state.doc.line(lineNum);
                view.dispatch({
                    selection: { anchor: line.from, head: line.from },
                });
                view.focus();

                // Second rAF to ensure layout is complete
                requestAnimationFrame(() => {
                    if (!view || !editorEl) return;
                    const scroller = editorEl.querySelector('.cm-scroller');
                    if (!scroller) return;
                    const coords = view.coordsAtPos(line.from);
                    if (!coords) return;
                    const viewportHeight = scroller.clientHeight;
                    const docHeight = scroller.scrollHeight;
                    // Place the line ~30% from the top of the viewport for context above
                    const targetScroll = coords.top + scroller.scrollTop - viewportHeight * 0.3;
                    scroller.scrollTop = Math.max(0, Math.min(targetScroll, docHeight - viewportHeight));
                });
            });
        }
    }

    function updateEditorContent() {
        if (view) {
            const current = view.state.doc.toString();
            if (current !== content) {
                view.dispatch({
                    changes: { from: 0, to: current.length, insert: content },
                    selection: { anchor: 0 },
                });
            }
        }
    }

    onMount(() => {
        loadFile();

        // On mobile, the keyboard changes the viewport height. Scroll cursor into view.
        function handleResize() {
            if (!view) return;
            const sel = view.state.selection.main;
            if (sel.anchor !== sel.head) return; // don't interrupt selections
            view.dispatch(
                { selection: { anchor: sel.anchor, head: sel.head } },
                { scrollIntoView: true },
            );
        }

        // Use visualViewport API for accurate keyboard tracking (Chrome Android)
        const vv = typeof visualViewport !== 'undefined' ? visualViewport : null;
        vv?.addEventListener('resize', handleResize, { passive: true });
        // Fallback for iOS Safari (uses window resize)
        window.addEventListener('resize', handleResize);

        return () => {
            if (searchApp) {
                unmount(searchApp);
                searchApp = null;
            }
            lspClient?.disconnect();
            lspTransport?.release();
            lspTransport = null;
            lspClient = null;
            view?.destroy();
            editorReady = false;
            vv?.removeEventListener('resize', handleResize);
            window.removeEventListener('resize', handleResize);
        };
    });

    // Re-create editor after baseline fetch completes so gitGutter has a comparison target.
    $effect(() => {
        const _loaded = originalContentLoaded;
        const _original = originalContent;
        if (!loading && !fileTooLarge && !fileNotFound && !view && editorEl && _loaded !== undefined) {
            requestAnimationFrame(() => {
                if (editorEl && !view) {
                    createEditor();
                }
            });
        }
    });

    // Initialize LSP after the file and editor are ready
    $effect(() => {
        const _editorReady = editorReady;
        const _loaded = originalContentLoaded;
        const _projPath = projectPath;
        const _projId = projectId;

        if (!_editorReady || !view || !_projId || !_projPath || _loaded === undefined) return;
        if (lspClient) return; // already initialized

        async function initLSP() {
            // Check user preference
            lspEnabled = await isLSPEnabled();
            if (!lspEnabled) return; // fall back to built-in autocompletion

            lspConnecting = true;
            fileUri = `file://${filePath}`;
            try {
                const virtTransport = await requestLSPChannel({
                    workspace: _projPath!,
                    filePath,
                    onDisconnected: () => {
                        lspReady = false;
                    },
                });
                lspTransport = virtTransport;

                lspClient = new LSPClient({
                    extensions: languageServerExtensions(),
                    rootUri: `file://${_projPath!}`,
                });

                await lspClient.connect(virtTransport).initializing;
                lspReady = true;

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
        const _lspEnabled = lspEnabled;

        // Only reconnect if we previously had a client but it's now disconnected
        if (!_lspClient || !_view || !_projPath || !_lspEnabled) return;
        if (_lspReady || _lspClient.connected || _lspConnecting) return; // still connected or connecting

        async function reconnectLSP() {
            lspConnecting = true;
            try {
                lspTransport?.release();
                lspTransport = null;
                _lspClient?.disconnect();

                const virtTransport = await requestLSPChannel({
                    workspace: _projPath!,
                    filePath,
                    onDisconnected: () => {
                        lspReady = false;
                    },
                });
                lspTransport = virtTransport;

                const newClient = new LSPClient({
                    extensions: languageServerExtensions(),
                    rootUri: `file://${_projPath!}`,
                });
                lspClient = newClient;

                await newClient.connect(virtTransport).initializing;
                lspReady = true;

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

    async function loadFile() {
        loading = true;
        error = '';
        fileTooLarge = false;
        fileNotFound = false;
        displayPath = displayPathProp || filePath.split('/').pop() || filePath;

        try {
            const params = new URLSearchParams({ path: filePath });
            if (projectId) params.set('project', projectId);
            if (taskId) params.set('task', taskId);
            const res = await fetch(`/api/fs/read?${params}`);
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
            content = data.content || '';

            // Fetch baseline for git gutter. Uses DiffViewer's baseRef so markers match the diff.
            // On failure (new file), originalContent stays empty — all lines shown as added.
            const refToUse = baseRef || 'HEAD';
            const headParams = new URLSearchParams({ path: filePath, ref: refToUse });
            if (projectId) headParams.set('project', projectId);
            if (taskId) headParams.set('task', taskId);
            try {
                const headRes = await fetch(`/api/fs/read?${headParams}`);
                if (headRes.ok) {
                    const headData = await headRes.json();
                    originalContent = headData.content || '';
                    originalContentLoaded = true;
                } else {
                    originalContent = '';
                    originalContentLoaded = false;
                }
            } catch {
                originalContent = '';
                originalContentLoaded = false;
            }
            savedContent = content; // disk matches current
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    async function handleSave() {
        if (saving || !hasChanges || fileTooLarge || fileNotFound) return;
        saving = true;
        try {
            const csrfToken = await ensureCsrfToken();
            const params = new URLSearchParams();
            if (projectId) params.set('project', projectId);
            if (taskId) params.set('task', taskId);
            const res = await fetch(`/api/fs/write?${params}`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
                },
                body: JSON.stringify({ path: filePath, content }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({ error: 'Failed to save file' }));
                throw new Error(err.error || 'Failed to save file');
            }
            savedContent = content;
            if (view) {
                if (gitGutterState) {
                    gitGutterState.original = content;
                }
            }
        } catch (e) {
            dialogStore.showToast('Failed to save: ' + String(e), 'error');
        } finally {
            saving = false;
        }
    }

    function handleClose() {
        onClose(true);
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
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions — role="application" makes this an interactive landmark -->
<div class="diff-editor" role="application" tabindex="-1" onkeydown={handleKeyDown}>
    <!-- Header -->
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
                <button type="button" class="editor-icon-btn editor-fullscreen-toggle" onclick={onToggleFullscreen} title="Toggle fullscreen">
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

    <!-- Body -->
    <div class="editor-body">
        {#if loading}
            <div class="editor-loading">
                <svg class="spinner" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                    <path d="M12 2a10 10 0 0 1 10 10"/>
                </svg>
                <span>Loading file...</span>
            </div>
        {:else if error}
            <div class="editor-error">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                    <circle cx="12" cy="12" r="10"/>
                    <path d="M12 8v4M12 16h.01"/>
                </svg>
                <span>{error}</span>
            </div>
        {:else if fileNotFound}
            <div class="editor-placeholder">
                <p style="margin:0 0 8px">File not found</p>
                <p style="margin:0;font-size:0.75rem;color:var(--color-text-tertiary)">{displayPath || filePath}</p>
            </div>
        {:else if fileTooLarge}
            <div class="editor-large">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                    <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z"/>
                    <path d="M6 6l4 4M10 6l-4 4"/>
                </svg>
                <span>File too large to edit (max 1MB)</span>
            </div>
        {:else}
            <div class="editor-content" bind:this={editorEl}></div>
            <div class="search-container" bind:this={searchContainerEl}></div>
        {/if}
    </div>

    <!-- Footer -->
    {#if !loading && !error && !fileTooLarge && !fileNotFound}
        <EditorFooter
            {cursorLine}
            {cursorCol}
            {lineCount}
            charCount={content.length}
            {hasChanges}
            onFind={openSearchPanel}
        />
    {/if}
</div>

<!-- Unsaved changes confirmation dialog -->
{#if showConfirm}
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

<style>
    .diff-editor {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .editor-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 8px;
        flex-shrink: 0;
        background: var(--color-surface-2);
        border-bottom: 1px solid var(--color-border);
        gap: 8px;
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        flex: 1;
        color: var(--color-text-secondary);
    }
    .header-left svg {
        flex-shrink: 0;
        opacity: 0.6;
    }
    .file-path {
        font-size: 0.75rem;
        font-weight: 500;
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
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
        position: relative;
    }
    .btn-back:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    /* Unsaved indicator — yellow circle, X appears on hover */
    .btn-back .unsaved-circle {
        display: none;
    }
    .btn-back .close-x {
        display: block;
    }
    .btn-back.unsaved .unsaved-circle {
        display: block;
    }
    .btn-back.unsaved .close-x {
        display: none;
    }
    .btn-back.unsaved {
        color: var(--color-status-yellow, #f0c000);
    }
    .btn-back.unsaved:hover .unsaved-circle {
        display: none;
    }
    .btn-back.unsaved:hover .close-x {
        display: block;
    }
    .btn-back.unsaved:hover {
        color: var(--color-text-primary);
    }

    .editor-actions {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .editor-icon-btn {
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
    .editor-icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .editor-fullscreen-toggle {
        display: flex;
    }

    .btn-save {
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
    .btn-save:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-save:disabled {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        cursor: not-allowed;
    }
    .save-spinner {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }

    .editor-body {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
        min-height: 0;
        position: relative;
    }

    .editor-loading,
    .editor-error,
    .editor-large {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
    }
    .editor-error {
        color: var(--color-status-red);
    }
    .spinner {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    .editor-content {
        flex: 1;
        overflow: hidden;
        min-height: 0;
    }

    .search-container {
        position: absolute;
        bottom: 0;
        left: 0;
        right: 0;
        z-index: 100;
        pointer-events: none;
    }


    /* CodeMirror theme overrides to match our UI */
    .editor-content :global(.cm-editor) {
        height: 100%;
        font-size: 14px;
    }
    .editor-content :global(.cm-scroller) {
        font-family: var(--font-mono);
        font-size: 14px;
        line-height: 1.5;
        tab-size: 4;
    }
    .editor-content :global(.cm-gutters) {
        background: var(--color-surface-1);
        border-right: 1px solid var(--color-border);
        color: var(--color-text-tertiary);
        padding: 0 6px 0 0;
    }
    .editor-content :global(.cm-activeLineGutter) {
        color: #8B949E;
        background: rgba(255,255,255,0.06);
    }
    .editor-content :global(.cm-content) {
        caret-color: var(--color-text-primary);
    }

    .confirm-overlay {
        position: absolute;
        inset: 0;
        background: rgba(0, 0, 0, 0.3);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 10;
        border-radius: 6px;
    }
    .confirm-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px;
        width: 320px;
        max-width: 90%;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
    }
    .confirm-dialog h3 {
        margin: 0 0 6px;
        font-size: 0.9375rem;
    }
    .confirm-dialog p {
        margin: 0 0 16px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }
    .confirm-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }
    .btn-confirm-cancel {
        padding: 5px 14px;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .btn-confirm-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-confirm-discard {
        padding: 5px 14px;
        background: var(--color-status-red-dark);
        color: white;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .btn-confirm-discard:hover {
        opacity: 0.9;
    }
</style>
