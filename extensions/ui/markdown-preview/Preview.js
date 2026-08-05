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
    });

    // ── Wordgard type normalization ────────────────────────────────────────
    // Wordgard exports block/leaf types either as a Tag/Leaf (Plot.define,
    // Leaf.define — has a `.type`) or as the Type itself (Plot.Type.define,
    // Leaf.Type.define). Normalize so `node.type === nt(t.X)` works for both.
    const T = () => window.WordgardTypes;
    const nt = (window.MarkdownSerializer && window.MarkdownSerializer.nt) ||
        ((x) => (x && x.type) || x);
})();
