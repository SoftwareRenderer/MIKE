<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { getEditorDraft, setEditorDraft, clearEditorDraft } from '$lib/storage';
    import { EditorView, basicSetup } from 'codemirror';
    import { EditorState } from '@codemirror/state';
    import { syntaxHighlighting, HighlightStyle } from '@codemirror/language';
    import { tags } from '@lezer/highlight';
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
    import { getLanguageTreeFromView } from '$lib/highlighter';

    // GitHub Dark theme — matches app colors
    const githubDarkHighlight = HighlightStyle.define([
        { tag: tags.keyword, color: '#F97583' },
        { tag: tags.constant(tags.keyword), color: '#79C0FF' },
        { tag: tags.definition(tags.variableName), color: '#FFA657' },
        { tag: tags.variableName, color: '#FFA657' },
        { tag: tags.function(tags.variableName), color: '#D2A8FF' },
        { tag: tags.propertyName, color: '#79C0FF' },
        { tag: tags.string, color: '#A5D6FF' },
        { tag: tags.number, color: '#79C0FF' },
        { tag: tags.comment, color: '#8B949E', fontStyle: 'italic' },
        { tag: tags.operator, color: '#FF7B72' },
        { tag: tags.punctuation, color: '#C9D1D9' },
        { tag: tags.className, color: '#FFA657' },
        { tag: tags.typeName, color: '#FFA657' },
        { tag: tags.tagName, color: '#7EE787' },
        { tag: tags.attributeName, color: '#79C0FF' },
        { tag: tags.attributeValue, color: '#A5D6FF' },
    ]);

    const githubDarkTheme = EditorView.theme({
        '&': { backgroundColor: 'transparent', color: '#C9D1D9' },
        '&.cm-focused': { outline: 'none' },
        '.cm-scroller': {
            fontFamily: 'var(--font-mono)',
            fontSize: '0.75rem',
            lineHeight: '1.5',
            tabSize: 4,
        },
        '.cm-gutters': {
            background: 'var(--color-surface-1)',
            borderRight: '1px solid var(--color-border)',
            color: '#484F58',
            padding: '0 6px 0 0',
        },
        '.cm-activeLineGutter': {
            color: '#8B949E',
            backgroundColor: 'rgba(255,255,255,0.06)',
        },
        '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
        '.cm-selectionBackground': { backgroundColor: 'rgba(88,166,255,0.3)' },
        '&.cm-focused .cm-selectionBackground': { backgroundColor: 'rgba(88,166,255,0.3)' },
        '.cm-cursor': { borderLeftColor: '#C9D1D9' },
    });

    let { filePath, onFocused, onSymbolsUpdate, onHasChanges }: {
        filePath: string;
        onFocused: (focused: boolean) => void;
        onSymbolsUpdate: (symbols: Array<{ name: string; kind: string; line: number; indent: number }>) => void;
        onHasChanges: (hasChanges: boolean) => void;
    } = $props();

    let content = $state('');
    let loading = $state(false);
    let saving = $state(false);
    let error = $state('');
    let fileTooLarge = $state(false);
    let fileName = $state('');
    let editorFocused = $state(false);

    let hasChanges = $derived(content !== originalContent);
    let originalContent = $state('');

    // Notify parent when unsaved changes state changes
    $effect(() => {
        onHasChanges(hasChanges);
    });

    let editorEl: HTMLDivElement | null = null;
    let view: EditorView | null = null;

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
        };
        if (base === 'dockerfile' || base === 'makefile' || base === 'cmakeLists.txt') return null;
        const support = extMap[ext];
        return support ? support() : null;
    }

    function extractSymbols() {
        if (!view) return;
        const symbols = getLanguageTreeFromView(view);
        onSymbolsUpdate(symbols);
    }

    function createEditor() {
        if (!editorEl) return;
        const langSupport = getLanguageSupport(filePath);
        const extensions = [
            basicSetup,
            githubDarkTheme,
            syntaxHighlighting(githubDarkHighlight),
            EditorView.updateListener.of((update) => {
                if (update.docChanged) {
                    content = update.state.doc.toString();
                    // Persist unsaved draft
                    clearTimeout((view as any)._draftTimer);
                    (view as any)._draftTimer = setTimeout(() => setEditorDraft(filePath, content), 200);
                }
                if (update.selectionSet) {
                    // Extract symbols on selection change (debounced)
                    clearTimeout((view as any)._symbolTimer);
                    (view as any)._symbolTimer = setTimeout(extractSymbols, 300);
                }
            }),
            EditorState.tabSize.of(4),
            ...(langSupport ? [langSupport] : []),
        ];

        const state = EditorState.create({ doc: content, extensions });
        view = new EditorView({ state, parent: editorEl });

        // Track focus/blur via DOM events on the editor's dom element
        if (view.dom) {
            view.dom.addEventListener('focus', () => {
                editorFocused = true;
                onFocused(true);
            }, true);
            view.dom.addEventListener('blur', () => {
                editorFocused = false;
                onFocused(false);
            }, true);
        }

        // Extract symbols after a short delay
        setTimeout(extractSymbols, 500);
    }

    async function loadFile() {
        if (!filePath) {
            loading = false;
            content = '';
            originalContent = '';
            fileName = '';
            view?.destroy();
            view = null;
            return;
        }
        loading = true;
        error = '';
        fileTooLarge = false;
        fileName = filePath.split('/').pop() || filePath;

        // Destroy old editor
        view?.destroy();
        view = null;
        content = '';
        originalContent = '';

        try {
            const res = await fetch(`/api/fs/read?path=${encodeURIComponent(filePath)}`);
            if (!res.ok) {
                if (res.status === 413) {
                    fileTooLarge = true;
                    return;
                }
                const err = await res.json().catch(() => ({ error: 'Failed to read file' }));
                throw new Error(err.error || 'Failed to read file');
            }
            const data = await res.json();
            originalContent = data.content || '';
            // Use unsaved draft if available, otherwise use file content
            const draft = getEditorDraft(filePath);
            content = draft !== null ? draft : originalContent;
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }

        // Create editor after DOM has updated (loading=false renders the editor div)
        if (!error && !fileTooLarge) {
            await new Promise(resolve => requestAnimationFrame(resolve));
            if (editorEl && !view) {
                createEditor();
            }
        }
    }

    async function handleSave() {
        if (saving || !hasChanges || fileTooLarge || !filePath) return;
        saving = true;
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch('/api/fs/write', {
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
            originalContent = content;
            clearEditorDraft(filePath);
        } catch (e) {
            error = String(e);
        } finally {
            saving = false;
        }
    }

    // Handle jump-to-line events from symbol outline
    function handleJumpToLine(e: Event) {
        const line = (e as CustomEvent).detail.line;
        if (!view || line < 1) return;
        const totalLines = view.state.doc.lines;
        const lineNum = Math.min(line, totalLines);
        const lineObj = view.state.doc.line(lineNum);
        view.dispatch({ selection: { anchor: lineObj.from, head: lineObj.from } });
        view.focus();
        // Scroll into view
        requestAnimationFrame(() => {
            const coords = view.coordsAtPos(lineObj.from);
            if (coords) {
                editorEl?.querySelector('.cm-scroller')?.scrollTo({
                    top: (editorEl?.querySelector('.cm-scroller') as HTMLElement)?.scrollTop - (editorEl?.querySelector('.cm-scroller') as HTMLElement)?.clientHeight! / 3 + coords.top,
                    behavior: 'smooth',
                });
            }
        });
    }

    // Re-create editor when file changes
    $effect(() => {
        if (filePath) {
            loadFile();
        }
    });

    // Create editor after DOM is ready (editorEl is in a conditional block)
    $effect(() => {
        if (!loading && !fileTooLarge && !view && editorEl) {
            // Use rAF to ensure the DOM has been painted after the conditional block renders
            requestAnimationFrame(() => {
                if (editorEl && !view) {
                    createEditor();
                }
            });
        }
    });

    onMount(() => {
        if (editorEl) {
            editorEl.addEventListener('jump-to-line', handleJumpToLine);
        }
    });

    onDestroy(() => {
        view?.destroy();
        if (editorEl) {
            editorEl.removeEventListener('jump-to-line', handleJumpToLine);
        }
    });
</script>

<div class="branch-editor" class:has-file={!!filePath}>
    {#if filePath}
        <!-- File tab header -->
        <div class="editor-header">
            <div class="editor-tab">
                <span class="editor-filename" title={filePath}>{fileName}</span>
                {#if hasChanges}
                    <span class="editor-dirty" title="Unsaved changes">
                        <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
                            <circle cx="4" cy="4" r="3.5" fill="currentColor"/>
                        </svg>
                    </span>
                {/if}
            </div>
            <div class="editor-actions">
                <button
                    class="editor-save-btn"
                    class:saving={saving}
                    class:has-changes={hasChanges}
                    onclick={handleSave}
                    disabled={!hasChanges || saving || fileTooLarge}
                    title="Save (Ctrl+S)"
                >
                    {#if saving}Saving...{:else}Save{/if}
                </button>
            </div>
        </div>
    {/if}

    <!-- Editor content area -->
    <div class="editor-content">
        {#if loading}
            <div class="editor-placeholder">Loading file...</div>
        {:else if error}
            <div class="editor-error">{error}</div>
        {:else if fileTooLarge}
            <div class="editor-placeholder">File too large to edit (max 1MB)</div>
        {:else if !filePath}
            <div class="editor-placeholder">
                <svg width="32" height="32" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1" opacity="0.3" aria-hidden="true">
                    <path d="M3 1h6l4 4v8a1 1 0 01-1 1H3a1 1 0 01-1-1V2a1 1 0 011-1z"/>
                    <polyline points="9,1 9,5 13,5"/>
                </svg>
                <p>Select a file to edit</p>
            </div>
        {:else}
            <div class="editor-wrapper" bind:this={editorEl}></div>
        {/if}
    </div>
</div>

<style>
    .branch-editor {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .editor-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0 8px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
        min-height: 30px;
    }

    .editor-tab {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        font-size: 0.75rem;
        color: var(--color-text-primary);
    }

    .editor-filename {
        max-width: 300px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-family: var(--font-mono);
        font-size: 0.6875rem;
    }

    .editor-dirty {
        color: var(--color-status-yellow, #f0c000);
        display: inline-flex;
        align-items: center;
        line-height: 1;
    }

    .editor-actions {
        display: flex;
        gap: 4px;
    }

    .editor-save-btn {
        padding: 2px 8px;
        font-size: 0.6875rem;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        cursor: pointer;
        border: none;
    }
    .editor-save-btn:hover:not(:disabled) {
        background: var(--color-accent);
        color: white;
        opacity: 1;
    }
    .editor-save-btn:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .editor-save-btn.saving {
        color: var(--color-status-orange);
    }

    .editor-content {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
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
    .editor-placeholder svg {
        margin-bottom: 4px;
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
</style>
