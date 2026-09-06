/**
 * A language server's markdown, rendered through the client the app actually uses.
 *
 * `@codemirror/lsp-client` highlights a fenced block only if the tag resolves to a `Language`: either
 * the tag equals the editor's grammar name, or `highlightLanguage` answers it. Our grammars are all
 * named `mike-declared`, so the first never matches — and the hook was never set, so every hover
 * signature came out monochrome. So: a real `LSPClient` from the production config factory, a real
 * `EditorView` carrying the theme's `HighlightStyle`, and the real `hoverDocsHTML`.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { highlightingFor } from '@codemirror/language';
import { tags, type Tag } from '@lezer/highlight';
import { LSPClient, LSPPlugin } from '@codemirror/lsp-client';
import { lspClientConfig } from '$lib/utils/lsp-client-config';
import { hoverDocsHTML } from '$lib/utils/lsp-hover-actions';
import { darkSyntax } from '$lib/utils/codemirror-theme';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension, SyntaxSpec } from '$lib/types';

const GO_PROFILE: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [{ quote: '"', escapes: true }],
  pairs: [{ open: '{', close: '}' }, { open: '(', close: ')' }],
  keywords: ['func', 'package', 'return'],
  types: ['int', 'string', 'error'],
  numbers: true,
};

/** The signature gopls sends for `fmt.Println`, as markdown. */
const GOPLS_HOVER = '```go\nfunc Println(a ...any) (n int, err error)\n```\n\nPrintln formats using the default formats.';

const views: EditorView[] = [];

/** An editor wired the way `FileEditor` wires one. `withHook: false` builds the client as it was built
 *  before this existed — the bug, kept executable. */
function editorFor(filePath: string, languageId: string, withHook = true): EditorView {
  const client = new LSPClient(
    withHook
      ? lspClientConfig({
          extensions: [],
          rootUri: 'file:///repo',
          getFile: () => filePath,
          onMessage: () => {},
        })
      : { extensions: [], rootUri: 'file:///repo' },
  );
  const view = new EditorView({
    state: EditorState.create({
      doc: 'package main\n',
      extensions: [darkSyntax, client.plugin(`file://${filePath}`, languageId)],
    }),
  });
  views.push(view);
  return view;
}

function pluginFor(view: EditorView): LSPPlugin {
  const plugin = LSPPlugin.get(view);
  if (!plugin) throw new Error('no LSPPlugin on the view — the fixture is not wired like the app');
  return plugin;
}

/** The class this editor's theme gives a tag, so the tests never hard-code a generated `cm-*` name. */
function classOf(view: EditorView, tag: Tag): string {
  const cls = highlightingFor(view.state, [tag]);
  if (!cls) throw new Error(`the theme gives no class for ${tag}`);
  return cls.trim();
}

/** The class the rendered HTML put on `text`, or null when it got no span at all. */
function classOn(html: string, text: string): string | null {
  const escaped = text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`<span class="([^"]*)">${escaped}</span>`).exec(html);
  return match ? match[1] : null;
}

const mounted: HTMLElement[] = [];

/**
 * The block as the app renders it — inside the card's elements, attached to the document so the
 * stylesheet the editor mounted applies. jsdom resolves those rules, so this answers what an attribute
 * check cannot: does the class the renderer emitted actually paint.
 */
function mountedBlock(html: string): HTMLElement {
  const host = document.createElement('div');
  host.innerHTML = `<div class="cm-lsp-action-docs cm-lsp-documentation">${html}</div>`;
  document.body.appendChild(host);
  mounted.push(host);
  return host;
}

function colorOf(host: HTMLElement, selector: string): string {
  const el = host.querySelector<HTMLElement>(selector);
  if (!el) throw new Error(`nothing matched ${selector}`);
  return getComputedStyle(el).color;
}

beforeEach(() => {
  extensionManager.state.extensions = [];
  extensionManager.state.extensions.push({
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
          args: [],
          language: 'go',
          file_types: [{ extension: 'go', language_id: 'go', label: 'Go', syntax: 'go' }],
          syntax: { go: GO_PROFILE },
        },
      },
    },
    config: {},
    container_config: {},
    sandbox_config: { network_isolation: true, memory_limit: 256 },
    install_source: 'manual',
    enabled: true,
    running: false,
    installed_at: new Date().toISOString(),
    built_at: new Date().toISOString(),
  } as Extension);
});

afterEach(() => {
  while (mounted.length) mounted.pop()!.remove();
  while (views.length) views.pop()!.destroy();
});

describe('a hover card signature is painted like the code it shows', () => {
  it('colors the keyword the theme colors a keyword', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: GOPLS_HOVER } });
    expect(classOn(html, 'func'), html).toBe(classOf(view, tags.keyword));
    expect(classOn(html, 'int'), html).toBe(classOf(view, tags.typeName));
  });

  it('keeps the classes through the sanitizer', () => {
    // DOMPurify runs over the block: a stripped class attribute would reopen the bug silently.
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: GOPLS_HOVER } });
    expect(html).toContain('<pre>');
    expect(html.match(/<span class="[^"]+">/g) ?? []).not.toHaveLength(0);
    // Highlighting the block must not swallow the doc comment.
    expect(html).toContain('Println formats using the default formats.');
  });

  it('paints an unlabelled fence with the grammar of the file being hovered', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: '```\nfunc main() {}\n```' } });
    expect(classOn(html, 'func'), html).toBe(classOf(view, tags.keyword));
  });

  it('paints a fence whose language nothing installed declares, as that file', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: '```bash\nfunc old()\n```' } });
    expect(classOn(html, 'func'), html).toBe(classOf(view, tags.keyword));
  });

  it('paints a core-language fence with the core grammar, not the file\'s', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: '```json\n{"key": "value"}\n```' } });
    expect(classOn(html, '"value"'), html).toBe(classOf(view, tags.string));
  });

  it('leaves a fence that declares itself prose unstyled', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: '```text\nfunc not code\n```' } });
    expect(classOn(html, 'func'), html).toBeNull();
    expect(html).toContain('func not code');
  });

  it('paints the block, rather than only labelling it', () => {
    // A class in an attribute is not a color: these are the editor's own HighlightStyle rules.
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: GOPLS_HOVER } });
    const host = mountedBlock(html);
    const reference = document.createElement('span');
    reference.className = classOf(view, tags.keyword);
    host.appendChild(reference);
    const painted = colorOf(host, 'pre span'); // the `func` of the signature
    const expected = getComputedStyle(reference).color; // what the theme says a keyword looks like
    const prose = colorOf(host, 'pre'); // the block's own color, with no span under it
    expect(painted).toBe(expected);
    expect(painted).not.toBe(prose);
  });

  it('still escapes plaintext documentation', () => {
    const view = editorFor('/repo/x.go', 'go');
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'plaintext', value: 'a < b' } });
    expect(html).toContain('a &lt; b');
    expect(html).not.toContain('<span');
  });

  it('answers nothing when no hook is configured — the bug, executable', () => {
    // Same input and theme, client built as it was before `lspClientConfig`: `go` resolves to nothing.
    const view = editorFor('/repo/x.go', 'go', false);
    const html = hoverDocsHTML(pluginFor(view), { contents: { kind: 'markdown', value: GOPLS_HOVER } });
    expect(classOn(html, 'func'), html).toBeNull();
    expect(html).toContain('func Println(a ...any) (n int, err error)');
  });
});
