/**
 * Which grammar the editor loads for a file, and which language identifier the
 * language server is told about.
 *
 * Those are two different questions, and answering only one of them is how `.ts`
 * files ended up parsed as JavaScript:
 *
 *   • `getLanguageSupport()` picks the CodeMirror grammar (highlighting, folding,
 *     outline, sticky-scroll symbols). It also leaks into the protocol: when
 *     `LSPClient.plugin(uri)` is called *without* an explicit language id,
 *     `@codemirror/lsp-client` uses the editor's `Language.name` as the
 *     `textDocument/didOpen` languageId. `javascript()` is named `"javascript"`
 *     even for a `.ts` file — and `@replit/codemirror-lang-svelte` has no name at
 *     all (`""`).
 *   • `lspLanguageId()` is the LSP *well-known language identifier* for the file —
 *     what the server actually wants. For the JavaScript/TypeScript family that is
 *     a 4-way map (`typescript` / `typescriptreact` / `javascript` /
 *     `javascriptreact`), because typescript-language-server turns it into a
 *     tsserver `ScriptKind` and only the first `didOpen` for a URI takes effect.
 *     A wrong one makes tsserver parse the buffer with the wrong grammar:
 *     `.ts` as `javascript` → "'interface' declarations can only be used in
 *     TypeScript files."; `.tsx`/`.jsx` as `typescript` → a wall of `',' expected.`;
 *     `.js` as `typescript` → type-checked as TypeScript.
 *   • `detectLanguage()` ($lib/utils/lsp-connection-manager) answers a third
 *     question: which installed extension owns the file, i.e. the manifest
 *     `language` the backend routes on. One extension owns ts+tsx+js+jsx under
 *     language `"typescript"`, so the routing language and the wire languageId are
 *     deliberately different values — collapsing them breaks routing
 *     (`lookupExtension("javascript")` finds nothing).
 */
import type { LanguageSupport } from '@codemirror/language';
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
import { detectLanguage } from './lsp-connection-manager';

function extensionOf(filePath: string): string {
  return filePath.split('.').pop()?.toLowerCase() || '';
}

/**
 * LSP well-known identifiers for the files one TypeScript extension owns. These
 * must be per-extension: the manifest language ("typescript") cannot express
 * "this is a .tsx file", and tsserver needs the difference.
 */
const LSP_IDS: Record<string, string> = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  jsx: 'javascriptreact',
  ts: 'typescript', mts: 'typescript', cts: 'typescript',
  tsx: 'typescriptreact',
};

/** The `textDocument/didOpen` languageId for a file — never empty, never a display name. */
export function lspLanguageId(filePath: string): string {
  return LSP_IDS[extensionOf(filePath)] ?? detectLanguage(filePath);
}

/**
 * CodeMirror language for a file, or null when there is nothing better than plain
 * text. Extensionless build files are deliberately null: their names carry the
 * type, and a wrong grammar is worse than none (it silently produces `⚠` nodes
 * that corrupt folding and the outline).
 */
export function getLanguageSupport(filePath: string): LanguageSupport | null {
  const ext = extensionOf(filePath);
  const base = filePath.split('/').pop()?.toLowerCase() || '';
  const extMap: Record<string, () => LanguageSupport> = {
    js: javascript, mjs: javascript, cjs: javascript,
    jsx: () => javascript({ jsx: true }),
    // The TypeScript dialect is not cosmetic: without it these files are parsed
    // with the JavaScript grammar, which both mis-highlights them and names them
    // "javascript" to the language server.
    ts: () => javascript({ typescript: true }),
    tsx: () => javascript({ jsx: true, typescript: true }),
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
    svelte: svelte,
    tmpl: goTemplate, gohtml: goTemplate,
  };
  if (base === 'dockerfile' || base === 'makefile' || base === 'cmakelists.txt') return null;
  const support = extMap[ext];
  return support ? support() : null;
}
