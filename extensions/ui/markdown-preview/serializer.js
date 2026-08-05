/**
 * Markdown ↔ Wordgard serialization for the markdown-preview extension.
 *
 * Pure functions shared by Preview.js (production) and the extension's tests
 * (tests/wordgard-wysiwyg.mjs). This is the single source of truth for both
 * the markdown→HTML renderer and the Wordgard-doc→markdown serializer.
 *
 * Depends on the Wordgard globals (window.WordgardTypes etc.) being loaded
 * first (see ./lib/wordgard.js). Exposes `window.MarkdownSerializer`.
 */
(() => {
    'use strict';

    const T = () => window.WordgardTypes;

    // Wordgard exports block/leaf types either as a Tag/Leaf (Plot.define,
    // Leaf.define — has a `.type`) or as the Type itself (Plot.Type.define,
    // Leaf.Type.define). `node.is()` compares against the Type, so normalize.
    function nt(x) { return (x && x.type) || x; }

    // ── Wordgard doc → Markdown serializer ──────────────────────────────────
    // Walks the Wordgard document tree (schema from fullSchema) and emits
    // markdown. This is the reverse of renderMarkdown below.

    function escapeText(s) {
        return String(s).replace(/\\/g, '\\\\').replace(/`/g, '\\`');
    }

    function escapeBrackets(s) {
        return String(s || '').replace(/\\/g, '\\\\').replace(/\]/g, '\\]');
    }

    function textOf(node) {
        return node.textContent ? node.textContent() : '';
    }

    /** Signature of a text node's active marks, used to merge adjacent runs. */
    function markSig(node) {
        const t = T();
        return [
            node.mark(nt(t.Strong)), node.mark(nt(t.Emphasis)), node.mark(nt(t.Code)),
            node.mark(nt(t.Link)), node.mark(nt(t.Strikethrough)), node.mark(nt(t.Underline)),
            node.mark(nt(t.Superscript)), node.mark(nt(t.Subscript)),
        ].map(v => (v === undefined ? '0' : '1')).join('');
    }

    function inlineContent(block) {
        const t = T();
        const nodes = block.content;
        const parts = [];
        let i = 0;
        while (i < nodes.length) {
            const node = nodes[i];
            if (node.isText) {
                // Merge adjacent text leaves that share the same marks so we
                // don't emit `**a****b**` for a single emphasized run.
                const sig = markSig(node);
                let text = '';
                let j = i;
                while (j < nodes.length && nodes[j].isText && markSig(nodes[j]) === sig) {
                    text += nodes[j].param;
                    j++;
                }
                parts.push(applyMarks(node, text));
                i = j;
            } else if (node.type === nt(t.Image)) {
                parts.push('![' + escapeBrackets(node.mark(nt(t.ImageAlt)) || '') + '](' + node.param + ')');
                i++;
            } else if (node.type === nt(t.LineBreak)) {
                parts.push('  \n');
                i++;
            } else {
                parts.push(escapeText(textOf(node)));
                i++;
            }
        }
        return parts.join('');
    }

    function applyMarks(node, text) {
        const t = T();
        let out = escapeText(text);
        if (node.mark(nt(t.Code)) !== undefined) out = '`' + out + '`';
        if (node.mark(nt(t.Strikethrough)) !== undefined) out = '~~' + out + '~~';
        if (node.mark(nt(t.Strong)) !== undefined) out = '**' + out + '**';
        if (node.mark(nt(t.Emphasis)) !== undefined) out = '*' + out + '*';
        const link = node.mark(nt(t.Link));
        if (link !== undefined) out = '[' + out + '](' + link + ')';
        return out;
    }

    function blockToMarkdown(node) {
        const t = T();
        if (node.type === nt(t.Heading)) {
            return '#'.repeat(node.tag.param || 1) + ' ' + inlineContent(node);
        }
        if (node.type === nt(t.Paragraph)) return inlineContent(node);
        if (node.type === nt(t.CodeBlock)) {
            const lang = node.mark(nt(t.CodeBlockLanguage)) || '';
            return '```' + lang + '\n' + textOf(node) + '\n```';
        }
        if (node.type === nt(t.Blockquote)) {
            const inner = node.content.map(blockToMarkdown).join('\n\n');
            return inner.split('\n').map(l => '> ' + l).join('\n');
        }
        if (node.type === nt(t.BulletList)) return listToMarkdown(node, '-');
        if (node.type === nt(t.OrderedList)) return listToMarkdown(node, 'ordered');
        if (node.type === nt(t.HorizontalRule)) return '---';
        if (node.type === nt(t.Figure)) {
            return '![' + escapeBrackets(node.mark(nt(t.ImageAlt)) || '') + '](' + node.param + ')';
        }
        if (node.type === nt(t.Table)) return tableToMarkdown(node);
        return textOf(node);
    }

    /** Table row node → its array of cell markdown strings. */
    function rowCells(row) {
        const t = T();
        const cells = [];
        for (const cell of row.content) {
            if (!(cell.type === nt(t.HeaderCell) || cell.type === nt(t.Cell))) continue;
            // Escape pipes so they stay literal cell content, not column breaks.
            cells.push(inlineContent(cell).replace(/\|/g, '\\|'));
        }
        return cells;
    }

    // Serialize a Wordgard Table node back to a GitHub-style pipe table. The
    // first row is treated as the header; body rows follow. ColSpan/RowSpan
    // have no Markdown representation, so spanned cells are emitted as-is
    // (their span is lost on round-trip, which is expected for Markdown).
    function tableToMarkdown(table) {
        const rows = table.content.map(rowCells);
        if (rows.length === 0) return '';
        const numCols = Math.max.apply(null, rows.map(r => r.length));
        if (numCols === 0) return '';
        const pad = row => {
            const out = row.slice(0, numCols);
            while (out.length < numCols) out.push('');
            return out;
        };
        const [header, ...body] = rows;
        const lines = ['| ' + pad(header).join(' | ') + ' |'];
        lines.push('| ' + Array(numCols).fill('---').join(' | ') + ' |');
        for (const row of body) lines.push('| ' + pad(row).join(' | ') + ' |');
        return lines.join('\n');
    }

    function listToMarkdown(list, markerKind) {
        const t = T();
        const items = [];
        let n = markerKind === 'ordered' ? (list.tag.param || 1) : 1;
        for (const item of list.content) {
            if (!(item.type === nt(t.ListItem) || item.type === nt(t.InlineListItem))) continue;
            const marker = markerKind === 'ordered' ? n++ + '. ' : '- ';
            const isInlineItem = item.inlineContent || item.type === nt(t.InlineListItem);
            const content = isInlineItem ? inlineContent(item) : item.content.map(blockToMarkdown).join('\n\n');
            const indented = content.split('\n')
                .map((line, idx) => (idx === 0 ? marker : ' '.repeat(marker.length)) + line)
                .join('\n');
            items.push(indented);
        }
        return items.join('\n');
    }

    function docToMarkdown(doc) {
        return doc.content.map(blockToMarkdown).filter(s => s.trim() !== '').join('\n\n') + '\n';
    }

    // ── Minimal Markdown renderer (markdown → HTML) ─────────────────────────
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

    // Emit a <pre> that carries the fenced block's language via data-language,
    // which Wordgard maps to its CodeBlockLanguage mark (preserving the
    // language across the WYSIWYG round-trip).
    function openPre(lang) {
        return lang ? '<pre data-language="' + lang + '"><code>' : '<pre><code>';
    }

    // ── GitHub-style pipe tables (markdown → HTML) ─────────────────────────
    // Splits a pipe-table row into trimmed cell strings. Escaped pipes (\|)
    // stay within their cell and are un-escaped (a literal pipe in the cell).
    function splitTableRow(line) {
        const cells = [];
        let cur = '';
        for (let i = 0; i < line.length; i++) {
            const ch = line[i];
            if (ch === '\\' && i + 1 < line.length && line[i + 1] === '|') {
                cur += '|';
                i++;
            } else if (ch === '|') {
                cells.push(cur.trim());
                cur = '';
            } else {
                cur += ch;
            }
        }
        cells.push(cur.trim());
        // Strip the empty cells produced by leading/trailing outer pipes.
        if (cells.length && cells[0] === '') cells.shift();
        if (cells.length && cells[cells.length - 1] === '') cells.pop();
        return cells;
    }

    // A delimiter row separates a table header from its body. Each cell is
    // dashes with optional leading/trailing colons (alignment markers).
    function isDelimiterRow(line) {
        const cells = splitTableRow(line);
        return cells.length > 0 && cells.every(c => /^:?-+:?$/.test(c.replace(/\s/g, '')));
    }

    // Build a <table> that Wordgard parses into its Table/TableRow/Cell types.
    // The header row maps to <th> (HeaderCell); body rows to <td> (Cell). The
    // <tbody> wrapper matches Wordgard's Table structure (table > tbody).
    function renderTable(headers, body) {
        const head = '<tr>' + headers.map(c => '<th>' + inline(c) + '</th>').join('') + '</tr>';
        const rows = body.map(r => '<tr>' + r.map(c => '<td>' + inline(c) + '</td>').join('') + '</tr>').join('');
        return '<table><tbody>' + head + rows + '</tbody></table>';
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
                    out.push(openPre(codeLang) + escapeHtml(codeBuf.join('\n')) + '</code></pre>');
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

            // GitHub-style pipe table: a header row (containing a pipe)
            // immediately followed by a delimiter row. Consume the header,
            // delimiter, and all following pipe rows as one <table> block.
            if (line.includes('|') && i + 1 < lines.length && isDelimiterRow(lines[i + 1])) {
                flushParagraph();
                const header = splitTableRow(line);
                i++; // skip the delimiter row
                const body = [];
                while (i + 1 < lines.length) {
                    const next = lines[i + 1].trim();
                    if (next === '' || !next.includes('|')) break;
                    body.push(splitTableRow(next));
                    i++;
                }
                out.push(renderTable(header, body));
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
            out.push(openPre(codeLang) + escapeHtml(codeBuf.join('\n')) + '</code></pre>');
        }
        return out.join('\n');
    }

    window.MarkdownSerializer = {
        renderMarkdown: renderMarkdown,
        docToMarkdown: docToMarkdown,
        nt: nt,
    };
})();
