/**
 * Markdown Preview — WYSIWYG editing via Wordgard.
 *
 * Adds a "Preview" button to the editor footer when a Markdown file is open.
 * Clicking it toggles between the code editor and a rich-text (WYSIWYG) view.
 */
(() => {
    'use strict';

    const MARKDOWN_EXT = /\.(md|markdown|mdown|mkdn|mkd)$/i;
    const BUTTON_ID = 'markdown-preview';

    // Shared serializer/renderer (./serializer.js, loaded before this file).
    const { renderMarkdown, docToMarkdown } = window.MarkdownSerializer || {};

    let previewActive = false;
    let wgEditor = null;          // current Wordgard editor instance
    let lastPushed = '';          // last markdown we pushed to the host

    /** Apply the host's current palette to this iframe's :root. */
    function applyTheme(variables) {
        if (!variables) return;
        const root = document.documentElement;
        for (const key of Object.keys(variables)) {
            if (key.startsWith('--')) root.style.setProperty(key, variables[key]);
        }
    }

    function isMarkdown() {
        const path = (extHost.context && extHost.context.filePath) || '';
        return MARKDOWN_EXT.test(path);
    }

    function setButtonActive(active) {
        // No label → only updates the active state (keeps "Preview").
        return extHost.editor.addFooterButton(BUTTON_ID, undefined, active);
    }

    function reorganizeMenu(container) {
        const menubar = container.querySelector('wg-menubar');
        if (!menubar) return false;
        const more = menubar.querySelector('wg-submenu[title="More"]');
        if (!more) return false; // toolbar not mounted yet
        const strike = more.querySelector('button[title="Toggle strikethrough"]');
        if (strike) more.parentNode.insertBefore(strike, more); // top-level bar
        more.remove();
        return true;
    }

    function observeMenuReorg(container) {
        // The toolbar mounts asynchronously after Wordgard.create; reorganize
        // once it appears, then stop observing.
        const mo = new MutationObserver(() => {
            if (reorganizeMenu(container)) mo.disconnect();
        });
        mo.observe(container, { childList: true, subtree: true });
        if (reorganizeMenu(container)) mo.disconnect();
    }

    // ── WYSIWYG editor lifecycle ─────────────────────────────────────────

    function destroyWysiwyg() {
        if (wgEditor) {
            // Wordgard has no destroy() — removing the editor's DOM element
            // triggers its disconnect callback and releases the editor.
            try { if (wgEditor.dom && wgEditor.dom.remove) wgEditor.dom.remove(); } catch (e) {}
            wgEditor = null;
        }
        const c = document.getElementById('wg-container');
        if (c) c.remove();
        lastPushed = '';
    }

    function createWysiwyg(markdown) {
        destroyWysiwyg();
        if (!window.WordgardEditor || !window.WordgardSchema || !window.WordgardHistory) {
            extHost.error('Markdown Preview: Wordgard bundle failed to load');
            return;
        }
        const { Wordgard, menuBar } = window.WordgardEditor;
        const { fullSchema } = window.WordgardSchema;
        const { history } = window.WordgardHistory;
        const { tables } = window.WordgardTable;
        const { GardState } = window.WordgardState;
        const t = T();

        const container = document.createElement('div');
        container.id = 'wg-container';
        document.body.appendChild(container);

        wgEditor = Wordgard.create({
            parent: container,
            doc: renderMarkdown(markdown),
            config: [
                fullSchema(),
                // fullSchema() adds CodeBlock but not its CodeBlockLanguage
                // mark; without it the fenced code language can't be parsed
                // (or preserved) across the WYSIWYG round-trip.
                GardState.schemaElement.of(nt(t.CodeBlockLanguage)),
                ...tables(),
                history(),
                // Formatting toolbar (bold/italic/headings/lists/etc.).
                // menuBar() returns an array of extensions, so spread it.
                ...menuBar(),
                Wordgard.scrolling('100%'),
                Wordgard.updateListener.of((update) => {
                    if (!update.docChanged) return;
                    const md = docToMarkdown(update.state.doc);
                    lastPushed = md;
                    extHost.editor.setContent(md).catch(() => {});
                }),
            ],
        });
        observeMenuReorg(container);
    }

    /**
     * Index (0-based) of the heading whose markdown source line matches
     * `line` (1-based), or -1 if that line isn't a heading. Used to map an
     * outline symbol's source line onto the Wordgard document's headings,
     * which the renderer emits in the same order (one per source heading).
     */
    function headingIndexForLine(markdown, line) {
        const lines = String(markdown || '').split('\n');
        let index = -1;
        // Track fenced code blocks (``` or ~~~) so bash comments ("# comment")
        // inside code blocks are not counted as headings — matching how the
        // host's outline (highlighter.ts extractMarkdownHeadings) builds the
        // symbol list. Otherwise the index is inflated by code-block lines and
        // the outline jumps to the wrong heading.
        let inCodeBlock = false;
        let fenceChar = null;
        let fenceLength = 0;
        for (let i = 0; i < lines.length; i++) {
            const raw = lines[i];
            const fenceMatch = /^(`{3,}|~{3,})(.*)$/.exec(raw);
            if (fenceMatch) {
                const char = fenceMatch[1][0];
                const len = fenceMatch[1].length;
                if (!inCodeBlock) {
                    // Opening fence
                    inCodeBlock = true;
                    fenceChar = char;
                    fenceLength = len;
                } else if (char === fenceChar && len >= fenceLength) {
                    // Closing fence (same char, at least as long)
                    inCodeBlock = false;
                    fenceChar = null;
                    fenceLength = 0;
                }
                continue;
            }
            // Skip heading detection inside code blocks
            if (inCodeBlock) continue;
            if (/^(#{1,6})\s+/.test(raw.trim())) {
                index++;
                if (i + 1 === line) return index;
            }
        }
        return -1;
    }

    /**
     * Scroll the WYSIWYG editor to the heading at markdown source `line`.
     * Triggered by the host's symbol outline (jump-to-line). When the preview
     * is off this is a no-op — the host's CodeMirror view handles the jump.
     */
    function scrollToSourceLine(line) {
        if (!previewActive || !wgEditor || !window.WordgardEditor) return;
        const { Wordgard } = window.WordgardEditor;
        extHost.editor.getContent().then(function (res) {
            if (!previewActive || !wgEditor) return; // toggled off meanwhile
            const index = headingIndexForLine(res && res.content, line);
            if (index < 0) return; // not a heading line
            const doc = wgEditor.state.doc;
            const t = T();
            let pos = -1;
            let count = 0;
            doc.iterate(0, doc.length, function (node, nodePos) {
                if (node.type === nt(t.Heading)) {
                    if (count === index) pos = nodePos;
                    count++;
                }
            });
            if (pos < 0) return;
            // Align the heading near the top of the viewport (like the host's
            // code-view jump-to-line), not just minimally into view.
            wgEditor.dispatch({ effects: [Wordgard.scrollIntoView(pos, { y: 'start', yMargin: 8 })] });
        }).catch(function () {});
    }

    /** Replace the Wordgard document from new markdown (external change). */
    function replaceDocFromMarkdown(markdown) {
        if (!wgEditor || !window.WordgardDoc) return;
        const { parse } = window.WordgardDoc;
        const template = document.createElement('template');
        template.innerHTML = renderMarkdown(markdown);
        let doc;
        try {
            doc = parse(wgEditor.state.schema, template.content);
        } catch (e) {
            extHost.log('warn', 'Markdown Preview: parse failed: ' + (e && e.message));
            return;
        }
        wgEditor.dispatch({
            changes: { from: 0, to: wgEditor.state.doc.length, insert: doc.content },
        });
    }

    async function togglePreview() {
        previewActive = !previewActive;
        document.body.classList.toggle('preview-on', previewActive);
        document.body.classList.toggle('preview-off', !previewActive);
        try {
            if (previewActive) {
                const { content } = await extHost.editor.getContent();
                createWysiwyg(content);
                await extHost.editor.setOverlay(true);
            } else {
                destroyWysiwyg();
                await extHost.editor.setOverlay(false);
            }
            await setButtonActive(previewActive);
        } catch (err) {
            extHost.error('Preview failed: ' + (err && err.message ? err.message : err));
        }
    }

    function onFooterClick(data) {
        if (data && data.id === BUTTON_ID) togglePreview();
    }

    function onContentChange(data) {
        if (!previewActive || !wgEditor) return;
        if (!data || typeof data.content !== 'string') return;
        // Ignore the echo of our own setContent push.
        if (data.content === lastPushed) return;
        // The document changed elsewhere (code view, undo, reload, etc.).
        replaceDocFromMarkdown(data.content);
    }

    // Add the footer button, retrying while the editor footer is still being
    // rendered (the file loads shortly after the iframe mounts).
    async function addButton(retries = 8) {
        for (let attempt = 0; attempt <= retries; attempt++) {
            try {
                await extHost.editor.addFooterButton(BUTTON_ID, 'Preview', false);
                return;
            } catch (e) {
                if (attempt === retries) {
                    extHost.log('warn', 'Markdown Preview: could not add footer button: ' + (e && e.message));
                    return;
                }
                await new Promise((r) => setTimeout(r, 250));
            }
        }
    }

    extHost.ready().then(function () {
        if (!isMarkdown()) return;
        extHost.theme.get()
            .then(function (res) { applyTheme(res && res.variables); })
            .catch(function () {});
        extHost.on('theme.update', function (data) {
            if (data && data.variables) applyTheme(data.variables);
        });
        addButton();
        extHost.on('editor.footerButtonClick', onFooterClick);
        extHost.on('editor.contentChange', onContentChange);
        extHost.on('editor.jumpToLine', function (data) {
            if (data && typeof data.line === 'number') scrollToSourceLine(data.line);
        });
    });

    // ── Wordgard type normalization ────────────────────────────────────────
    // Wordgard exports block/leaf types either as a Tag/Leaf (Plot.define,
    // Leaf.define — has a `.type`) or as the Type itself (Plot.Type.define,
    // Leaf.Type.define). Normalize so `node.type === nt(t.X)` works for both.
    const T = () => window.WordgardTypes;
    const nt = (window.MarkdownSerializer && window.MarkdownSerializer.nt) ||
        ((x) => (x && x.type) || x);
})();
