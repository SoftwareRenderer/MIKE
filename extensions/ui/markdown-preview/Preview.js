/**
 * Markdown Preview
 *
 * Adds a "Preview" button to the editor footer when a Markdown file is open.
 * Clicking it toggles between the code editor and a rendered preview of the
 * document.
 */
(() => {
    'use strict';

    const MARKDOWN_EXT = /\.(md|markdown|mdown|mkdn|mkd)$/i;
    const BUTTON_ID = 'markdown-preview';

    let previewActive = false;

    function isMarkdown() {
        const path = (extHost.context && extHost.context.filePath) || '';
        return MARKDOWN_EXT.test(path);
    }

    function setButtonActive(active) {
        // No label → only updates the active state (keeps "Preview").
        return extHost.editor.addFooterButton(BUTTON_ID, undefined, active);
    }

    function renderIntoPreview(content) {
        const el = document.getElementById('preview');
        if (el) el.innerHTML = renderMarkdown(content);
    }

    async function togglePreview() {
        previewActive = !previewActive;
        document.body.classList.toggle('preview-on', previewActive);
        document.body.classList.toggle('preview-off', !previewActive);
        try {
            if (previewActive) {
                const { content } = await extHost.editor.getContent();
                renderIntoPreview(content);
                await extHost.editor.setOverlay(true);
            } else {
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
        if (!previewActive) return;
        if (data && typeof data.content === 'string') renderIntoPreview(data.content);
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
                            console.log('test');
        if (!isMarkdown()) return;

        const preview = document.createElement('div');
        preview.id = 'preview';
        preview.className = 'md-preview';
        document.body.appendChild(preview);

        addButton();
        extHost.on('editor.footerButtonClick', onFooterClick);
        extHost.on('editor.contentChange', onContentChange);
    });

    // ── Minimal Markdown renderer ─────────────────────────────────────────
    // Renders a practical subset of CommonMark. Source HTML is escaped so
    // document content is always treated as text, never markup.

    function escapeHtml(s) {
        return String(s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;');
    }

    function inline(text) {
        let t = escapeHtml(text);
        // code spans
        t = t.replace(/`([^`\n]+)`/g, function (m, code) { return '<code>' + code + '</code>'; });
        // images ![alt](url)
        t = t.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
            function (m, alt, url, title) {
                return '<img src="' + url + '" alt="' + alt + '"' + (title ? ' title="' + title + '"' : '') + '>';
            });
        // links [text](url)
        t = t.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
            function (m, text, url, title) {
                return '<a href="' + url + '"' + (title ? ' title="' + title + '"' : '') + '>' + text + '</a>';
            });
        // bold then italic
        t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
        t = t.replace(/__([^_]+)__/g, '<strong>$1</strong>');
        t = t.replace(/(^|[^*])\*([^*\s][^*]*?)\*(?![*])/g, '$1<em>$2</em>');
        t = t.replace(/(^|[^_])_([^_\s][^_]*?)_(?![_])/g, '$1<em>$2</em>');
        return t;
    }

    function renderMarkdown(src) {
        const lines = String(src || '').split('\n');
        const out = [];
        let para = [];
        let codeLang = null;
        let codeBuf = [];

        const flushParagraph = function () {
            if (para.length === 0) return;
            out.push('<p>' + para.map(function (l) { return inline(l.trim()); }).join(' ') + '</p>');
            para = [];
        };

        for (let i = 0; i < lines.length; i++) {
            const raw = lines[i];
            const line = raw.trim();

            // Fenced code block
            const fence = line.match(/^```([\w+-]*)\s*$/);
            if (fence) {
                flushParagraph();
                if (codeLang !== null) {
                    out.push('<pre><code>' + escapeHtml(codeBuf.join('\n')) + '</code></pre>');
                    codeLang = null;
                    codeBuf = [];
                } else {
                    codeLang = fence[1] || '';
                }
                continue;
            }
            if (codeLang !== null) {
                codeBuf.push(raw);
                continue;
            }

            // Blank line ends a paragraph.
            if (line === '') { flushParagraph(); continue; }

            // ATX headings
            const heading = line.match(/^(#{1,6})\s+(.*)$/);
            if (heading) {
                flushParagraph();
                const level = heading[1].length;
                out.push('<h' + level + '>' + inline(heading[2]) + '</h' + level + '>');
                continue;
            }

            // Horizontal rule: 3+ of the same char (-, *, or _), spaces ignored.
            const hrChars = line.replace(/\s+/g, '');
            if (hrChars.length >= 3 && /^[-*_]+$/.test(hrChars) && new Set(hrChars).size === 1) {
                flushParagraph();
                out.push('<hr>');
                continue;
            }

            // Blockquote
            if (line.startsWith('>')) {
                flushParagraph();
                const quoteLines = [];
                while (i < lines.length) {
                    const q = lines[i].replace(/^\s*>\s?/, '');
                    if (q === '') { quoteLines.push(''); }
                    else quoteLines.push(q);
                    if (i + 1 < lines.length && !lines[i + 1].trim().startsWith('>')) break;
                    i++;
                }
                out.push('<blockquote>' + inline(quoteLines.join('\n').trim()) + '</blockquote>');
                continue;
            }

            // Unordered list
            const ul = line.match(/^([-*+])\s+(.*)$/);
            if (ul) {
                flushParagraph();
                out.push('<ul>');
                while (i < lines.length) {
                    const item = lines[i].trim().match(/^([-*+])\s+(.*)$/);
                    if (!item) break;
                    out.push('<li>' + inline(item[2]) + '</li>');
                    i++;
                }
                i--;
                out.push('</ul>');
                continue;
            }

            // Ordered list
            const ol = line.match(/^\d+\.\s+(.*)$/);
            if (ol) {
                flushParagraph();
                out.push('<ol>');
                while (i < lines.length) {
                    const item = lines[i].trim().match(/^\d+\.\s+(.*)$/);
                    if (!item) break;
                    out.push('<li>' + inline(item[1]) + '</li>');
                    i++;
                }
                i--;
                out.push('</ol>');
                continue;
            }

            // Plain paragraph line
            para.push(raw);
        }

        flushParagraph();
        if (codeLang !== null) {
            out.push('<pre><code>' + escapeHtml(codeBuf.join('\n')) + '</code></pre>');
        }
        return out.join('\n');
    }
})();
