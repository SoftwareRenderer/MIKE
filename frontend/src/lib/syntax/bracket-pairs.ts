/**
 * Bracket pair colorization from a declared spec, following the rule VS Code uses: a pair's color is
 * its nesting level, so an opener always matches the closer it opened and a reader can see which
 * `}` belongs to which `{`. Nothing here knows a language — the brackets come from the manifest's
 * `pairs`, and the colors from the theme, so light and dark differ the way VS Code's do.
 */
import { Decoration, ViewPlugin } from '@codemirror/view';
import type { DecorationSet, EditorView, PluginValue, ViewUpdate } from '@codemirror/view';
import { highlightTree, tagHighlighter, tags } from '@lezer/highlight';
import type { Tree } from '@lezer/common';
import type { EditorState, Text } from '@codemirror/state';
import type { DelimiterPair, SyntaxSpec } from '$lib/types';
import { TokenWalker, opaqueTokens } from './stream-tokens';

/** How many colors the cycle holds — VS Code's `editorBracketPairColorization` uses three. */
export const BRACKET_LEVELS = 3;

/** The class for one level. The theme owns the color; this only says which slot of the cycle. */
export const bracketClass = (level: number): string => `cm-bracket-l${level}`;

/**
 * How far the level scan runs from the top of the document.
 *
 * A pair's level is the depth of everything opened above it, so answering that for the visible window
 * means reading the document above it. There is no incremental bracket model here (VS Code keeps one;
 * we have a syntax tree, not a bracket index), so past this much text the scan costs more per keystroke
 * than the color is worth and nothing is colored — which is also what VS Code does with a document it
 * considers too large. Deliberately larger than the fold scan's window: a fold only needs its own line
 * and body, while a level needs the file.
 */
export const BRACKET_SCAN_LIMIT = 1_000_000;

/** One bracket and its level: an opener and the closer it opened carry the same level. */
export interface BracketLevel {
  from: number;
  to: number;
  level: number;
}

/** A range that is not code — a comment or a string. */
interface OpaqueRange {
  from: number;
  to: number;
}

/**
 * The scan itself, over anything that can answer "which character is here" and "which ranges are not
 * code": an editor reads through its `Text`, the diff view through a parsed string, and both must get
 * the same level for the same bracket.
 */
function scanBracketLevels(
  pairs: DelimiterPair[],
  from: number,
  to: number,
  charAt: (pos: number) => string,
  opaque: readonly OpaqueRange[],
): BracketLevel[] {
  const opens = new Map(pairs.map((p) => [p.open, p]));
  const closes = new Map(pairs.map((p) => [p.close, p.open]));
  const walk = new TokenWalker(opaque.map((r) => ({ name: '', from: r.from, to: r.to })));
  const stack: { opens: string; level: number }[] = [];
  const out: BracketLevel[] = [];

  let pos = from;
  while (pos < to) {
    const jumped = walk.skip(pos);
    if (jumped) {
      pos = jumped;
      continue;
    }
    const ch = charAt(pos);
    const opener = opens.get(ch);
    if (opener) {
      const level = stack.length % BRACKET_LEVELS;
      stack.push({ opens: ch, level });
      out.push({ from: pos, to: pos + 1, level });
    } else if (closes.has(ch)) {
      const top = stack[stack.length - 1];
      if (top && closes.get(ch) === top.opens) {
        stack.pop();
        out.push({ from: pos, to: pos + 1, level: top.level });
      }
    }
    pos++;
  }
  return out;
}

/**
 * Every bracket in `[from, to)` with its level, in document order.
 *
 * Brackets inside a comment or a string are not brackets and get nothing, a closer that opens nothing
 * (or closes the wrong thing) gets nothing rather than a borrowed color, and an opener never closed
 * keeps the level it opened at — that is the answer the file gave before it ran out of text.
 */
export function bracketPairLevels(
  state: EditorState,
  spec: SyntaxSpec,
  from = 0,
  to = state.doc.length,
): BracketLevel[] {
  const pairs = spec.pairs || [];
  if (!pairs.length || to - from > BRACKET_SCAN_LIMIT) return [];
  return scanBracketLevels(
    pairs,
    from,
    to,
    (pos) => state.sliceDoc(pos, pos + 1),
    opaqueTokens(state, from, to, BRACKET_SCAN_LIMIT),
  );
}

/** Comment and string nodes, by tag rather than by node name, so any grammar answers the same way. */
const NOT_CODE = tagHighlighter([
  { tag: tags.comment, class: 'opaque' },
  { tag: tags.string, class: 'opaque' },
]);

/**
 * The same answer for a parsed tree and its source: the diff view has neither an editor state nor a
 * live syntax tree to query, only what `lang.parser.parse` handed it.
 */
export function bracketLevelsForTree(
  tree: Tree,
  pairs: DelimiterPair[],
  source: string,
): BracketLevel[] {
  if (!pairs.length || source.length > BRACKET_SCAN_LIMIT) return [];
  const opaque: OpaqueRange[] = [];
  highlightTree(tree, NOT_CODE, (from, to) => opaque.push({ from, to }));
  return scanBracketLevels(pairs, 0, source.length, (pos) => source[pos], opaque);
}

const MARKS = Array.from({ length: BRACKET_LEVELS }, (_, level) =>
  Decoration.mark({ class: bracketClass(level) }),
);

/**
 * Colorize the brackets the spec declares. A `ViewPlugin` rather than tree decorations because the
 * answer is not a property of a node — a stream grammar has no bracket nodes — and it is only worth
 * computing for what the user can see.
 */
export function specBracketPairColorization(spec: SyntaxSpec) {
  if (!(spec.pairs || []).length) return [];

  return ViewPlugin.fromClass(
    class implements PluginValue {
      decorations: DecorationSet = Decoration.none;
      /**
       * The levels found so far, and how far down the document they reach. Recomputed when the buffer
       * changes or the view is scrolled past what has already been read, because those are the only
       * two things that can change an answer.
       */
      private found: { doc: Text; through: number; levels: BracketLevel[] } | null = null;

      constructor(private readonly view: EditorView) {
        this.refresh();
      }

      update(update: ViewUpdate): void {
        if (update.docChanged || update.viewportChanged) this.refresh();
      }

      private refresh(): void {
        const { state, visibleRanges } = this.view;
        if (!visibleRanges.length) {
          this.decorations = Decoration.none;
          return;
        }
        const seenTo = visibleRanges[visibleRanges.length - 1].to;
        const scanTo = Math.min(seenTo, BRACKET_SCAN_LIMIT);
        if (!this.found || this.found.doc !== state.doc || this.found.through < scanTo) {
          this.found = {
            doc: state.doc,
            through: scanTo,
            levels: bracketPairLevels(state, spec, 0, scanTo),
          };
        }
        // Only what is on screen gets a decoration: a document a scroll away keeps its color the
        // moment it comes into view, and pays nothing for the part that is not.
        const marks = [];
        for (const range of visibleRanges) {
          for (const b of this.found.levels) {
            if (b.to > range.from && b.from < range.to) marks.push(MARKS[b.level].range(b.from, b.to));
          }
        }
        this.decorations = Decoration.set(marks, true);
      }
    },
    { decorations: (value) => value.decorations },
  );
}
