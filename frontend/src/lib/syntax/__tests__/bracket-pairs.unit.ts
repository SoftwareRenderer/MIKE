/**
 * Bracket pair colorization: which bracket is at which nesting level, and which brackets are not
 * brackets at all. The colors themselves belong to the theme; this is the answer it styles.
 */
import { describe, it, expect } from 'vitest';
import { EditorState } from '@codemirror/state';
import type { SyntaxSpec } from '$lib/types';
import { buildLanguageSupport } from '../spec-language';
import { BRACKET_SCAN_LIMIT, bracketPairLevels } from '../bracket-pairs';

const SPEC: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [
    { quote: '"', escapes: true },
    { quote: '`', multiline: true },
  ],
  pairs: [
    { open: '{', close: '}' },
    { open: '(', close: ')' },
    { open: '[', close: ']' },
  ],
  keywords: ['func', 'if'],
  types: ['int'],
  numbers: true,
};

/** The brackets in the document, as `[character, level]`, in document order. */
function brackets(doc: string, spec: SyntaxSpec = SPEC): Array<[string, number]> {
  const state = EditorState.create({ doc, extensions: [buildLanguageSupport(spec, 'fixture')] });
  return bracketPairLevels(state, spec).map((b) => [doc.slice(b.from, b.to), b.level]);
}

const levels = (doc: string, spec?: SyntaxSpec) => brackets(doc, spec).map(([, level]) => level);
const chars = (doc: string, spec?: SyntaxSpec) => brackets(doc, spec).map(([ch]) => ch).join('');

describe('bracket pair levels', () => {
  it('gives a pair its nesting depth, so an opener and its closer always match', () => {
    // The shape from the bug report: ( at the top level, [ ] one inside it, and the block back out.
    expect(brackets('func f(a []int) {')).toEqual([
      ['(', 0],
      ['[', 1],
      [']', 1],
      [')', 0],
      ['{', 0],
    ]);
  });

  it('counts across lines, because a block is not a line', () => {
    const doc = 'func f(a []int) {\n  if (x) {\n  }\n}';
    expect(levels(doc)).toEqual([
      0, 1, 1, 0, 0, // func f(a []int) {
      1, 1, 1, //   if (x) {
      1, //   }
      0, // }
    ]);
    // The closer that ends the function is the same color as the brace that started it.
    expect(brackets(doc).slice(-1)).toEqual([['}', 0]]);
  });

  it('cycles over the theme slots rather than running out of colors', () => {
    expect(levels('(((((((')).toEqual([0, 1, 2, 0, 1, 2, 0]);
    expect(levels('(((())))')).toEqual([0, 1, 2, 0, 0, 2, 1, 0]);
  });

  it('does not see a bracket in a comment or a string', () => {
    expect(chars('f(a) // g( {')).toBe('()');
    expect(chars('s = "[";\nh(b)')).toBe('()'); // the [ inside the quotes is content, not a bracket
    expect(chars('/* { */ x()')).toBe('()');
    expect(chars('t = `{\n}`;\nz()')).toBe('()');
  });

  it('leaves a closer that opens nothing uncolored, and keeps the level an opener took', () => {
    expect(brackets('a)')).toEqual([]); // nothing was opened
    expect(brackets('f(a]')).toEqual([['(', 0]]); // the ] closes something that is not there
    expect(brackets('f(a')).toEqual([['(', 0]]); // the outer one never met its closer, and keeps the level it opened at
    expect(brackets('f((a)')).toEqual([
      ['(', 0],
      ['(', 1],
      [')', 1],
    ]);
  });

  it('offers nothing for a spec that declares no pairs', () => {
    const { pairs, ...noPairs } = SPEC;
    expect(pairs).toBeTruthy();
    expect(bracketPairLevels(EditorState.create({ doc: 'f(a)', extensions: [buildLanguageSupport(noPairs, 'fixture')] }), noPairs)).toEqual([]);
  });

  it('stops rather than scanning an enormous document', () => {
    const state = EditorState.create({ doc: 'f(a)', extensions: [buildLanguageSupport(SPEC, 'fixture')] });
    expect(bracketPairLevels(state, SPEC, 0, BRACKET_SCAN_LIMIT + 1)).toEqual([]);
  });
});

describe('the colorizer on a real view', () => {
  it('paints the brackets it can see at their level, and leaves a comment alone', async () => {
    const { EditorView } = await import('@codemirror/view');
    const doc = 'func f(a []int) {\n  return a[0]\n}\n// ( [ {\n';
    const view = new EditorView({ doc, extensions: [buildLanguageSupport(SPEC, 'fixture')] });
    const count = (level: number) => view.dom.querySelectorAll(`.cm-bracket-l${level}`).length;
    try {
      // Line 1 gives ( ) { at the outer level and [ ] one inside it; line 2's slice brackets sit
      // inside the function body, so they are one deep too; line 3 closes the body. Line 4 is a
      // comment, and its three brackets are not brackets.
      expect(count(0)).toBe(4);
      expect(count(1)).toBe(4);
      expect(count(2)).toBe(0);
    } finally {
      view.destroy();
    }
  });

  it('offers nothing when the spec declares no pairs', async () => {
    const { EditorView } = await import('@codemirror/view');
    const { pairs, ...noPairs } = SPEC;
    const view = new EditorView({ doc: 'f(a) { g(b) }', extensions: [buildLanguageSupport(noPairs, 'fixture')] });
    try {
      expect(view.dom.querySelectorAll('[class*="cm-bracket-l"]').length).toBe(0);
      expect(view.dom.textContent).toContain('f(a) { g(b) }'); // the text is there, unstyled
    } finally {
      view.destroy();
    }
  });
});
