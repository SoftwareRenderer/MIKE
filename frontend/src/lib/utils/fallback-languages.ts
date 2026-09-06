/**
 * The only language table left in the core: languages no LSP extension covers, whose grammar is
 * structural rather than lexical — JSON nesting, Markdown blocks, HTML tags, CSS declarations,
 * YAML indentation, Go templates. Consulted only when no extension declares the file type.
 * Files with no suffix (Dockerfile, Makefile) fall out of it and open as plain text.
 */
import type { LanguageSupport } from '@codemirror/language';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { yaml } from '@codemirror/lang-yaml';
import { sql } from '@codemirror/lang-sql';
import { goTemplate } from '$lib/gotemplate';
import { fileSuffix } from './file-suffix';

/**
 * Bare lowercase suffix → grammar.
 *
 * A `Map` rather than an object literal because the key comes off a filename, and an object literal
 * answers inherited keys as well as its own: `foo.constructor` used to resolve to `Object`, which is
 * truthy, so `getLanguageSupport` handed it to CodeMirror and the editor failed to open the file at
 * all ("Unrecognized extension value in extension set"). A Map answers only what is in the table.
 */
const GRAMMARS = new Map<string, () => LanguageSupport>([
  ['json', json],
  ['md', markdown],
  ['markdown', markdown],
  ['html', html],
  ['htm', html],
  ['css', css],
  ['scss', css],
  ['sass', css],
  ['less', css],
  ['yaml', yaml],
  ['yml', yaml],
  ['sql', sql],
  ['tmpl', goTemplate],
  ['gohtml', goTemplate],
]);

/**
 * The core fallback grammar for a bare lowercase suffix, or null for plain text. A markdown fence tag
 * names the same grammars as a file suffix, so one table answers both.
 */
export function fallbackLanguageForTag(tag: string): LanguageSupport | null {
  return GRAMMARS.get(tag)?.() ?? null;
}

/** The core fallback grammar for a path, or null for plain text. */
export function fallbackLanguage(filePath: string): LanguageSupport | null {
  return fallbackLanguageForTag(fileSuffix(filePath));
}
