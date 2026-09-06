/**
 * What the installed extensions declare about a file: its grammar, its `didOpen` languageId and its
 * status-bar label. The three must agree, because the id decides how the server parses the buffer.
 * Also answers what grammar a code fence in a language server's documentation gets.
 * See "Language Knowledge Lives in Extensions" in AGENTS.md.
 */
import type { Language, LanguageSupport } from '@codemirror/language';
import { buildLanguageSupport, specHasContent } from '$lib/syntax/spec-language';
import type { LSPConfig, LSPFileType, SyntaxSpec } from '$lib/types';
import { fallbackLanguage, fallbackLanguageForTag } from './fallback-languages';
import { fileBase, fileSuffix } from './file-suffix';
import { extensionManager } from '$lib/services/extension-manager.svelte';

/**
 * Grammar name for every spec-built language. Deliberately not a valid language id, so a call site
 * that forgets the explicit id gets "no provider for 'mike-declared'" instead of wrong diagnostics.
 */
const DECLARED_GRAMMAR_NAME = 'mike-declared';

/** What the installed extensions say about one file. */
export interface ResolvedFile {
  /** The `textDocument/didOpen` languageId, or `''` when no extension owns this file. */
  languageId: string;
  /** Status-bar label ("TypeScript JSX"), or `''` when nothing declared one. */
  label: string;
  /** The highlighting profile the extension declared, or null (fallback grammar or plain text). */
  spec: SyntaxSpec | null;
  /** The manifest `language` the backend routes the file to, or `''` when unrouted. */
  language: string;
  /**
   * Whether the extension that owns this file has its editor LSP features on — the gate that decides
   * whether a channel may open for it. Separate from everything above: a grammar is data the manifest
   * declares, and the toggle is sold as "diagnostics, completions, go-to-definition, and hover", not
   * as colors. Only `lsp-connection-manager.isLSPAvailable` acts on it.
   */
  editorLSP: boolean;
}

/** One declaration an enabled extension makes about one of its file types. */
interface Declaration {
  lsp: LSPConfig;
  fileType: LSPFileType;
}

/**
 * The first enabled extension whose declaration matches, with the file type within it.
 *
 * "First owner wins" is one rule and lives in one place: a file resolved by one loop and a fence tag
 * by another is two rules that can drift apart, which is what these two used to be.
 */
function findDeclaration(matches: (lsp: LSPConfig, fileType: LSPFileType) => boolean): Declaration | null {
  for (const ext of extensionManager.getEnabledLSPExtensions()) {
    const lsp = ext.manifest.capabilities?.lsp;
    if (!lsp?.file_types?.length) continue;
    const fileType = lsp.file_types.find((ft) => matches(lsp, ft));
    if (fileType) return { lsp, fileType };
  }
  return null;
}

/** The extension (and the file type within it) that owns `filePath`, among the enabled extensions. */
function declaredFor(filePath: string): (Declaration & { editorLSP: boolean }) | null {
  const base = fileBase(filePath);
  const suffix = fileSuffix(filePath);
  // A file named after the language ("go") reaches its server, as it does on the Go side.
  const namedAfterLanguage = (lsp: LSPConfig) =>
    !suffix && !!lsp.language && base === lsp.language.toLowerCase();

  const found = findDeclaration(
    (lsp, ft) =>
      (!!suffix && ft.extension === suffix) ||
      (namedAfterLanguage(lsp) && ft.language_id === lsp.language),
  );
  return found ? { ...found, editorLSP: found.lsp.editor_enabled !== false } : null;
}

/** Everything the editor needs to know about one file's language; undeclared files get no id. */
export function resolveFile(filePath: string): ResolvedFile {
  const declared = declaredFor(filePath);
  if (!declared) {
    return { languageId: '', label: '', spec: null, language: '', editorLSP: false };
  }
  const { lsp, fileType, editorLSP } = declared;
  const spec = fileType.syntax ? lsp.syntax?.[fileType.syntax] ?? null : null;
  return {
    languageId: fileType.language_id || lsp.language,
    label: fileType.label || '',
    spec,
    language: lsp.language || fileType.language_id,
    editorLSP,
  };
}

/** One grammar built per declared spec, since specs are shared across the manifest's file types. */
const builtLanguages = new WeakMap<SyntaxSpec, LanguageSupport>();

/** The grammar for a declared profile, built once per profile; null when it declares none. */
function grammarForSpec(spec: SyntaxSpec | null | undefined): LanguageSupport | null {
  if (!specHasContent(spec)) return null;
  const cached = builtLanguages.get(spec);
  if (cached) return cached;
  const built = buildLanguageSupport(spec, DECLARED_GRAMMAR_NAME);
  builtLanguages.set(spec, built);
  return built;
}

/**
 * The grammar for a file: its extension's declared profile, else the core fallback for languages
 * nothing covers, else plain text. A declared type with no profile opens as the fallback or plain text.
 * No LSP gate applies here — this reads the manifest, it does not talk to a server, so a file keeps
 * its colors when the user switches that extension's editor features off.
 */
export function getLanguageSupport(filePath: string): LanguageSupport | null {
  return grammarForSpec(resolveFile(filePath).spec) ?? fallbackLanguage(filePath);
}

/** Markdown's own way of saying "this block is prose, not code". */
const PLAIN_FENCE_TAGS = new Set(['text', 'txt', 'plain', 'plaintext']);

/**
 * The declaration that owns a fence tag, matched on the names a language server tags its own blocks
 * with: `extension`, `language_id`, `language`. No alias table — an undeclared tag (`golang`, `bash`)
 * resolves to nothing here and falls through to the file's own grammar. Every enabled extension
 * answers, gate or no gate: painting a code block is the same data read it is anywhere else.
 */
function declaredForTag(tag: string): Declaration | null {
  return findDeclaration(
    (lsp, ft) =>
      ft.extension?.toLowerCase() === tag ||
      ft.language_id?.toLowerCase() === tag ||
      (!!lsp.language && lsp.language.toLowerCase() === tag),
  );
}

/**
 * The grammar for a code fence in a language server's documentation (hover, completion, signature
 * help). `filePath` is the file the documentation is about, and it covers the two cases a tag cannot:
 * an unlabelled fence, and a language no installed extension declares. Both are highlighted as the
 * file they came from. An explicit `text` fence asks for prose and gets no grammar.
 */
export function getGrammarForFence(tag: string, filePath: string): Language | null {
  const name = (tag || '').trim().toLowerCase();
  if (PLAIN_FENCE_TAGS.has(name)) return null;
  if (name) {
    const declared = declaredForTag(name);
    const spec = declared ? declared.lsp.syntax?.[declared.fileType.syntax ?? ''] ?? null : null;
    const grammar = grammarForSpec(spec) ?? fallbackLanguageForTag(name);
    if (grammar) return grammar.language;
  }
  return getLanguageSupport(filePath)?.language ?? null;
}
