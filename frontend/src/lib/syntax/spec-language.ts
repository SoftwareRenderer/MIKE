/**
 * The generic syntax engine: a `SyntaxSpec` from an LSP extension's manifest, turned into a
 * CodeMirror language — tokenizer, bracket and region folding, indentation, bracket pair colorization,
 * Ctrl-/ and auto-close. Nothing here knows a language; see "Language Knowledge Lives in Extensions".
 */
import {
  Language,
  LanguageSupport,
  StreamLanguage,
  foldService,
  indentService,
} from '@codemirror/language';
import type { StringStream } from '@codemirror/language';
import type { EditorState } from '@codemirror/state';
import type { SyntaxSpec } from '$lib/types';
import { SCAN_LIMIT, TOK, TokenWalker, opaqueTokens } from './stream-tokens';
import { specBracketPairColorization } from './bracket-pairs';

const NUMBER = /0[xXbBoO][\da-fA-F_]+|(?:\d[\d_]*(?:\.\d+)?|\.\d+)(?:[eE][+-]?\d+)?/;
const WORD = /[\w$]/;
const OPERATOR_CHARS = '+-*/%=<>!&|^~?:';
/** Where the upper-case-first-is-a-type convention starts: an identifier's first letter. */
const UPPER_FIRST = /^\p{Lu}/u;

interface SpecState {
  blockComment: boolean;
  /** The quote of a string still open across lines, or null. */
  string: string | null;
}

const setOf = (words?: string[]) => new Set(words || []);

/** The tokenizer for one spec; every branch reads the spec, none names a language. */
function parserFor(spec: SyntaxSpec) {
  const keywords = setOf(spec.keywords);
  const types = setOf(spec.types);
  const constants = setOf(spec.constants);
  const builtins = setOf(spec.builtins);
  // Longest marker first, so a triple quote wins over the single quote it starts with.
  const lineComments = [spec.line_comment, ...(spec.line_comments || [])]
    .filter((m): m is string => Boolean(m))
    .sort((a, b) => b.length - a.length);
  const strings = [...(spec.strings || [])].sort((a, b) => b.quote.length - a.quote.length);
  // The word lists above are a language's vocabulary. What is left gets its colour from the shape of
  // the name, and an author may turn either shape rule off — hence defaults, not requirements.
  const callAsFunction = spec.call_as_function !== false;
  const capitalizedTypes = spec.capitalized_types !== false;

  return {
    startState: (): SpecState => ({ blockComment: false, string: null }),

    token(stream: StringStream, state: SpecState): string | null {
      // A block comment already open swallows the line up to its closer.
      if (state.blockComment && spec.block_comment) {
        const close = spec.block_comment.close;
        if (stream.skipTo(close)) {
          stream.pos += close.length;
          state.blockComment = false;
        } else {
          stream.skipToEnd();
        }
        return TOK.comment;
      }

      // A string already open across lines (template literal, triple-quoted string).
      if (state.string) {
        const delim = strings.find((s) => s.quote === state.string);
        readStringBody(stream, state, delim);
        return TOK.string;
      }

      if (stream.eatSpace()) return null;

      for (const marker of lineComments) {
        if (stream.match(marker, false)) {
          stream.pos += marker.length;
          stream.skipToEnd();
          return TOK.comment;
        }
      }
      if (spec.block_comment && stream.match(spec.block_comment.open, false)) {
        stream.pos += spec.block_comment.open.length;
        state.blockComment = true;
        return TOK.comment;
      }

      for (const delim of strings) {
        if (stream.match(delim.quote, false)) {
          stream.pos += delim.quote.length;
          readStringBody(stream, state, delim);
          return TOK.string;
        }
      }

      if (spec.numbers && stream.match(NUMBER, true)) return TOK.number;

      if (stream.eatWhile(WORD)) {
        const word = stream.current();
        if (keywords.has(word)) return TOK.keyword;
        if (types.has(word)) return TOK.type;
        if (constants.has(word)) return TOK.constant;
        if (builtins.has(word)) return TOK.builtin;
        // A stream grammar has no parse tree to ask, so the two conventions most of these languages
        // share answer instead: a name applied to arguments is a function, a name that starts
        // upper-case names a type. Everything else keeps the theme's default colour.
        if (callAsFunction && appliedToArguments(stream)) return TOK.func;
        if (capitalizedTypes && UPPER_FIRST.test(word)) return TOK.type;
        return null;
      }

      if (stream.eat(isOperator)) {
        stream.eatWhile(isOperator);
        return TOK.operator;
      }

      // Brackets and anything else get no token: the fold provider reads them from the document.
      stream.next();
      return null;
    },
  };
}

function isOperator(ch: string): boolean {
  return OPERATOR_CHARS.includes(ch);
}

/**
 * Is the next thing on this line, past nothing but spaces, an opening bracket? Looking no further
 * than the first non-space is what makes this cheap and safe: the `/` of a trailing comment, or the
 * `,` ending a parameter, stops the scan, so neither makes a name look like a call.
 */
function appliedToArguments(stream: StringStream): boolean {
  const { string, pos } = stream;
  for (let i = pos; i < string.length; i++) {
    const ch = string[i];
    if (ch === '(') return true;
    if (ch !== ' ' && ch !== '\t') return false;
  }
  return false;
}

/** Consume the rest of a string, honouring declared escapes and ending at the newline unless declared multiline. */
type StringDelim = NonNullable<SyntaxSpec['strings']>[number];

function readStringBody(stream: StringStream, state: SpecState, delim: StringDelim | undefined): void {
  const quote = delim?.quote ?? state.string ?? '';
  const escapes = delim?.escapes ?? false;
  for (;;) {
    if (stream.eol()) {
      // Only a declared multiline delimiter stays open, so one stray quote cannot recolour the file.
      state.string = delim?.multiline ? quote : null;
      return;
    }
    if (escapes && stream.match('\\', false)) {
      stream.pos += 2; // the escaped character belongs to the string, whatever it is
      continue;
    }
    if (stream.match(quote, false)) {
      stream.pos += quote.length;
      state.string = null;
      return;
    }
    stream.next();
  }
}

/** Build the declared language; `name` is the grammar name, never the LSP languageId. */
export function buildLanguageSupport(spec: SyntaxSpec, name: string): LanguageSupport {
  const language: Language = StreamLanguage.define<SpecState>({ name, ...parserFor(spec) });

  // The language data Ctrl-/ and auto-close read; the spec itself is not published.
  const data: Record<string, unknown> = {};
  if (spec.line_comment || spec.block_comment) {
    data.commentTokens = {
      ...(spec.line_comment ? { line: spec.line_comment } : {}),
      ...(spec.block_comment ? { block: spec.block_comment } : {}),
    };
  }
  if (spec.pairs?.length) data.closeBrackets = { brackets: spec.pairs.map((p) => p.open) };

  return new LanguageSupport(language, [
    language.data.of(data),
    specRegionFoldService(spec),
    specBracketFoldService(spec),
    specIndentService(spec),
    specBracketPairColorization(spec),
  ]);
}

/** Does this line leave a bracket open? Closing something it did not open counts as no. */
function lineOpensBracket(
  state: EditorState,
  lineStart: number,
  lineEnd: number,
  opening: Map<string, { open: string; close: string }>,
  closing: Set<string>,
): boolean {
  const walk = new TokenWalker(opaqueTokens(state, lineStart, lineEnd));
  const stack: string[] = [];
  let pos = lineStart;
  while (pos < lineEnd) {
    const jumped = walk.skip(pos);
    if (jumped) {
      pos = jumped;
      continue;
    }
    const ch = state.sliceDoc(pos, pos + 1);
    if (opening.has(ch)) {
      stack.push(ch);
    } else if (closing.has(ch)) {
      if (!stack.length) return false; // closes something opened earlier: this line opens nothing
      if (opening.get(stack[stack.length - 1])?.close !== ch) return false;
      stack.pop();
    }
    pos++;
  }
  return stack.length > 0;
}

/** Bracket folding from `pairs`: a line that leaves a bracket open folds its body to its closer. */
export function specBracketFoldService(spec: SyntaxSpec) {
  const pairs = spec.pairs || [];
  if (!pairs.length) return [];
  const opening = new Map(pairs.map((p) => [p.open, p]));
  const closing = new Set(pairs.map((p) => p.close));

  return foldService.of((state: EditorState, lineStart: number, lineEnd: number) => {
    // Cheap line-local check first; only a line that opens something pays for the wide scan below.
    if (!lineOpensBracket(state, lineStart, lineEnd, opening, closing)) return null;

    const end = Math.min(state.doc.length, lineStart + SCAN_LIMIT);
    const walk = new TokenWalker(opaqueTokens(state, lineStart, end));
    const stack: number[] = [];
    let pos = lineStart;
    while (pos < end) {
      const jumped = walk.skip(pos);
      if (jumped) {
        pos = jumped;
        continue;
      }
      const ch = state.sliceDoc(pos, pos + 1);
      if (opening.has(ch)) {
        stack.push(pos);
      } else if (closing.has(ch) && stack.length) {
        const top = state.sliceDoc(stack[stack.length - 1], stack[stack.length - 1] + 1);
        if (opening.get(top)?.close !== ch) return null; // unbalanced: not something to fold
        stack.pop();
        if (!stack.length) {
          const closeLine = state.doc.lineAt(pos).from;
          // A group that opens and closes on this line (`f()` in `function f() {`) is not the fold.
          if (closeLine > lineEnd) return { from: lineEnd, to: closeLine };
        }
      }
      pos++;
    }
    return null; // never closed: no fold, and no placeholder that would lie about one
  });
}

/** Region folding: a comment line with the region marker folds down to the line with the end marker. */
export function specRegionFoldService(spec: SyntaxSpec) {
  if (!spec.region) return [];
  const { open, close } = spec.region;

  return foldService.of((state: EditorState, lineStart: number, lineEnd: number) => {
    if (!isRegionMarker(state.sliceDoc(lineStart, lineEnd), open, spec)) return null;
    const end = Math.min(state.doc.length, lineStart + SCAN_LIMIT);
    for (const comment of opaqueTokens(state, lineEnd, end)) {
      if (comment.name !== TOK.comment) continue;
      const text = state.sliceDoc(comment.from, comment.to).split('\n', 1)[0];
      if (!isRegionMarker(text, close, spec)) continue;
      const to = state.doc.lineAt(comment.from).from;
      return to > lineEnd ? { from: lineEnd, to } : null;
    }
    return null;
  });
}

/** Match a region marker against a comment's own text, so prose mentioning `#region` does not match. */
function isRegionMarker(lineText: string, marker: string, spec: SyntaxSpec): boolean {
  let text = lineText.trim();
  for (const p of [spec.line_comment, spec.block_comment?.open, spec.block_comment?.close, '*']) {
    if (p && text.startsWith(p)) {
      text = text.slice(p.length).trimStart();
      break;
    }
  }
  return text === marker || text.startsWith(marker + ' ') || text.startsWith(marker + ':');
}

/** Indentation from `pairs`: one unit per unclosed opener on the previous line, one back on a closer. */
export function specIndentService(spec: SyntaxSpec) {
  const pairs = spec.pairs || [];
  if (!pairs.length) return [];
  const openers = new Set(pairs.map((p) => p.open));
  const closers = new Set(pairs.map((p) => p.close));

  return indentService.of((context, pos) => {
    const doc = context.state.doc;
    const line = doc.lineAt(pos);
    if (line.number === 1) return null;
    const prev = doc.line(line.number - 1);
    const walk = new TokenWalker(opaqueTokens(context.state, prev.from, line.to));

    let depth = 0;
    for (let p = prev.from; p < prev.to; p++) {
      const jumped = walk.skip(p);
      if (jumped) {
        p = jumped - 1;
        continue;
      }
      const ch = context.state.sliceDoc(p, p + 1);
      if (openers.has(ch)) depth++;
      else if (closers.has(ch) && depth > 0) depth--;
    }

    const restOfLine = line.text.slice(0, pos - line.from);
    const startsWithCloser = [...closers].some((c) => (restOfLine.trim() || line.text.trim()).startsWith(c));
    if (startsWithCloser && depth > 0) depth--;

    return depth * context.unit;
  });
}

/** A spec with nothing in it would give a blank editor; that is treated as "no grammar". */
export function specHasContent(spec: SyntaxSpec | undefined | null): spec is SyntaxSpec {
  if (!spec) return false;
  return Boolean(
    spec.line_comment || spec.block_comment || spec.strings?.length || spec.pairs?.length ||
      spec.keywords?.length || spec.types?.length || spec.constants?.length || spec.builtins?.length || spec.numbers ||
      spec.capitalized_types === true || spec.call_as_function === true,
  );
}
