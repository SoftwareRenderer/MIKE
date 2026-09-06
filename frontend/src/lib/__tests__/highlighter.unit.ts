import { describe, it, expect, beforeEach } from 'vitest';
import { clearHighlightCache, highlightLinesAsync } from '$lib/highlighter';
import { escapeHtml } from '$lib/utils/format';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension, SyntaxSpec } from '$lib/types';

describe('escapeHtml', () => {
    it('escapes every character that can break out of a text node or attribute', () => {
        // Diff/highlight output is interpolated into HTML strings that also
        // carry attributes, so quotes must be escaped too.
        expect(escapeHtml(`<img src=x onerror="alert('1')"> & more`))
            .toBe('&lt;img src=x onerror=&quot;alert(&#39;1&#39;)&quot;&gt; &amp; more');
    });

    it('does not double-escape ampersands it introduces', () => {
        expect(escapeHtml(`&'"`)).toBe('&amp;&#39;&quot;');
    });
});

/**
 * What a declared syntax profile paints in a diff. The editor and the diff read the same manifest, so
 * a name and a bracket must come back the same color in both — this is the diff half of that promise.
 */
const GO_PROFILE: SyntaxSpec = {
    line_comment: '//',
    block_comment: { open: '/*', close: '*/' },
    strings: [{ quote: '"', escapes: true }, { quote: '`', multiline: true }],
    pairs: [{ open: '{', close: '}' }, { open: '(', close: ')' }, { open: '[', close: ']' }],
    keywords: ['func', 'return'],
    types: ['int', 'bool', 'string'],
};

function addGoExt(opts: { editorEnabled?: boolean; enabled?: boolean } = {}): void {
    const ext = {
        id: 'gopls-1',
        name: 'gopls',
        version: '1.0.0',
        type: 'lsp',
        manifest: {
            display_name: 'gopls',
            description: '',
            author: 'Test',
            version: '1.0.0',
            capabilities: {
                lsp: {
                    command: 'gopls',
                    args: ['serve'],
                    language: 'go',
                    file_types: [{ extension: 'go', language_id: 'go', label: 'Go', syntax: 'go' }],
                    syntax: { go: GO_PROFILE },
                    // undefined = the manifest default (on); false is the Settings checkbox.
                    editor_enabled: opts.editorEnabled,
                },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 256 },
        install_source: 'manual',
        enabled: opts.enabled !== false,
        running: false,
        installed_at: new Date().toISOString(),
        built_at: new Date().toISOString(),
    } as unknown as Extension;
    extensionManager.state.extensions.push(ext);
}

/** One line, highlighted the way a diff renders it. */
async function highlighted(line: string): Promise<string> {
    clearHighlightCache();
    const [html] = await highlightLinesAsync([line], 'x.go');
    return html;
}

describe('diff highlighting from a declared profile', () => {
    beforeEach(() => {
        extensionManager.state.extensions = [];
        addGoExt();
    });

    it('colors a called name and an upper-case name, not only the declared word lists', async () => {
        const html = await highlighted('func formatChatHistory(messages []types.ChatMessage) string {');
        expect(html).toContain('<span class="hl-keyword">func</span>');
        expect(html).toContain('<span class="hl-function">formatChatHistory</span>');
        expect(html).toContain('<span class="hl-typename">ChatMessage</span>');
    });

    it('paints each bracket at its own nesting level', async () => {
        const html = await highlighted('func f(a []int) {');
        // ( ) and the block brace share the outer level; the slice brackets sit one inside it.
        expect(html.match(/hl-bracket-l0/g)?.length).toBe(3);
        expect(html.match(/hl-bracket-l1/g)?.length).toBe(2);
    });

    it('does not color a bracket that lives in a string or a comment', async () => {
        const html = await highlighted('s := f("[") // } ] (');
        // Only the call's own two brackets are brackets; the [, }, ] and ( are text.
        expect(html.match(/hl-bracket-l\d/g)?.length).toBe(2);
    });
});

describe('a diff is not gated by the editor-features switch', () => {
    // The reported bug, on the path that has no LSP client at all: a language whose grammar comes only
    // from an extension (there is no core fallback for Go, Python or the JS family) opened as plain
    // text the moment its extension's "Editor Features" box was unticked, because the same filter that
    // decides whether a channel may open was also answering the grammar.
    it('still colors the file when editor features are off', async () => {
        extensionManager.state.extensions = [];
        addGoExt({ editorEnabled: false });
        const html = await highlighted('func main() { return }');
        expect(html).toContain('<span class="hl-keyword">func</span>');
        expect(html).toContain('<span class="hl-function">main</span>');
    });

    it('colors nothing when the extension itself is switched off', async () => {
        // Disabling an extension is a different thing from its editor gate: the manifest claims nothing.
        extensionManager.state.extensions = [];
        addGoExt({ enabled: false });
        const html = await highlighted('func main() { return }');
        expect(html).not.toContain('hl-keyword');
    });
});
