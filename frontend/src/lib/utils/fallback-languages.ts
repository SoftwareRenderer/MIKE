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

/** Bare lowercase suffix (no dot) → grammar. */
const GRAMMARS: Record<string, () => LanguageSupport> = {
  json,
  md: markdown,
  markdown,
  html,
  htm: html,
  css,
  scss: css,
  sass: css,
  less: css,
  yaml,
  yml: yaml,
  sql,
  tmpl: goTemplate,
  gohtml: goTemplate,
};

/** The core fallback grammar for a path, or null for plain text. */
export function fallbackLanguage(filePath: string): LanguageSupport | null {
  const grammar = GRAMMARS[fileSuffix(filePath)];
  return grammar ? grammar() : null;
}
