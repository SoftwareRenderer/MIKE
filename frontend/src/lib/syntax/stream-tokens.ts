/**
 * The parts of a document that are not code, and the cursor that walks them. Every scan that reads a
 * document one character at a time — the fold providers, the indent service, the bracket-pair
 * colorizer — has to know that the bracket in `// if (a) {` and in `const s = "{"` is not a bracket,
 * so they share this one reader. It lives apart from the grammar engine because that engine imports
 * the colorizer and the colorizer imports this: neither may import the other.
 */
import { ensureSyntaxTree, syntaxTree } from '@codemirror/language';
import type { EditorState } from '@codemirror/state';

/**
 * Token names are CodeMirror tag names, so `defaultHighlightStyle` and the app theme apply. A stream
 * token name is a tag path, so `variableName.function` is `tags.function(tags.variableName)`.
 */
export const TOK = {
  comment: 'comment',
  string: 'string',
  keyword: 'keyword',
  type: 'typeName',
  constant: 'atom',
  builtin: 'className',
  number: 'number',
  operator: 'operator',
  func: 'variableName.function',
} as const;

/** Token classes that are not code — skipped by the fold, indent and bracket scans. */
const OPAQUE = new Set<string>([TOK.comment, TOK.string]);

/** How far a single fold/indent scan will run through the document. */
export const SCAN_LIMIT = 200_000;

/** A comment or string token — the parts of a document that are not code. */
export interface Token {
  name: string;
  from: number;
  to: number;
}

/**
 * Comment/string token ranges in `[from, to)`, in document order, at absolute offsets. `limit` is how
 * far this one call will look past `from`; the default is the fold/indent window, and a caller that
 * must see the whole document says so rather than quietly reading a truncated answer.
 */
export function opaqueTokens(state: EditorState, from: number, to: number, limit = SCAN_LIMIT): Token[] {
  const end = Math.min(to, from + limit);
  if (end <= from) return [];
  // Parse the window first: an unparsed tail reports its brackets as code, costing a fold at worst.
  ensureSyntaxTree(state, end, 20);
  const out: Token[] = [];
  const cursor = syntaxTree(state).cursor();
  cursor.moveTo(from, 1);
  for (;;) {
    if (cursor.from >= end) break;
    if (cursor.to > from && OPAQUE.has(cursor.name)) {
      out.push({ name: cursor.name, from: cursor.from, to: cursor.to });
      if (!cursor.next(false)) break; // step over its children: nothing opaque hides inside
      continue;
    }
    if (!cursor.next()) break;
  }
  return out;
}

/** Forward-only cursor over sorted token ranges: `skip` jumps past the token covering a position. */
export class TokenWalker {
  private i = 0;
  constructor(private readonly tokens: Token[]) {}

  skip(pos: number): number | null {
    while (this.i < this.tokens.length && this.tokens[this.i].to <= pos) this.i++;
    const token = this.tokens[this.i];
    return token && pos >= token.from && pos < token.to ? token.to : null;
  }
}
