/**
 * The one token table, checked against everything that has to know about a token.
 *
 * The bug this exists for: a declared profile tags a language's constants (`true`, `None`, `nil`) as
 * `tags.atom`, `atom` has no parent tag, and all three hand-written color lists carried
 * `constant(keyword)` instead — so constants painted nothing, in the editor and in the diff alike,
 * quietly. There is one table now; these checks run it through the real engine rather than reading the
 * table back to itself.
 */
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'fs';
import { join } from 'path';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { ensureSyntaxTree, syntaxTree, syntaxHighlighting } from '@codemirror/language';
import { highlightTree } from '@lezer/highlight';
import { json } from '@codemirror/lang-json';
import { buildLanguageSupport } from '../spec-language';
import { TOKEN_STYLES, buildHighlightStyle, diffHighlighter } from '../token-style';
import { TOK } from '../stream-tokens';
import type { SyntaxNode } from '@lezer/common';
import type { SyntaxSpec } from '$lib/types';

/** Where a token class may be painted — everything the one-stylesheet check walks. */
const SRC = join(process.cwd(), 'src', 'lib');

/**
 * A profile that exercises every token the stream engine can produce. If `TOK` grows, this grows:
 * the first test below fails until the fixture emits the new kind, which is the point — a token nobody
 * can produce is a token nobody can notice is unpainted.
 */
const EVERY_TOKEN: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [{ quote: '"', escapes: true }],
  pairs: [{ open: '(', close: ')' }],
  keywords: ['kw'],
  types: ['ty'],
  constants: ['con'],
  builtins: ['bui'],
  numbers: true,
  call_as_function: true,
  capitalized_types: false,
};
const SAMPLE = '// c\nkw ty con bui 42 "s" fn() 1+2';

const views: EditorView[] = [];

/** An editor with the fixture grammar and one theme's palette — the wiring `FileEditor` does. */
function editorFor(mode: 'dark' | 'light'): EditorView {
  const view = new EditorView({
    state: EditorState.create({
      doc: SAMPLE,
      extensions: [buildLanguageSupport(EVERY_TOKEN, 'fixture'), syntaxHighlighting(buildHighlightStyle(mode))],
    }),
  });
  ensureSyntaxTree(view.state, view.state.doc.length, 50);
  const host = document.createElement('div');
  document.body.appendChild(host);
  host.appendChild(view.dom);
  views.push(view);
  return view;
}

/** Every leaf of the fixture parse: its token name (`keyword`, `atom`, …) and its text. */
function leaves(): Array<{ token: string; text: string }> {
  const state = EditorState.create({ doc: SAMPLE, extensions: [buildLanguageSupport(EVERY_TOKEN, 'fixture')] });
  ensureSyntaxTree(state, state.doc.length, 50);
  const out: Array<{ token: string; text: string }> = [];
  const walk = (node: SyntaxNode) => {
    const first = node.firstChild;
    if (!first) {
      const text = state.sliceDoc(node.from, node.to);
      // A stream grammar's "tree" is its tokens: the node name IS the token name (`keyword`, `atom`).
      if (text.trim()) out.push({ token: node.name, text });
      return;
    }
    for (let child: SyntaxNode | null = first; child; child = child.nextSibling) walk(child);
  };
  walk(syntaxTree(state).topNode);
  return out;
}

/**
 * What each token the stream engine can emit must be painted as. This is the contract between
 * `stream-tokens.ts` (which names the tokens) and `token-style.ts` (which colors them), and the
 * reason it is spelled out: a tag can inherit another tag's color and nobody notices. With no row of
 * its own, `tags.atom` resolved through `tags.keyword`, so every declared language's constants came
 * out in the keyword color — a mislabelling that looks exactly like a decision.
 */
const TOKEN_DIFF_CLASS: Record<string, string> = {
  [TOK.comment]: 'hl-comment',
  [TOK.string]: 'hl-string',
  [TOK.keyword]: 'hl-keyword',
  [TOK.type]: 'hl-typename',
  [TOK.constant]: 'hl-atom',
  [TOK.builtin]: 'hl-classname',
  [TOK.number]: 'hl-number',
  [TOK.operator]: 'hl-operator',
  [TOK.func]: 'hl-function',
};

/** The color the table says `token` gets, in the form `getComputedStyle` reports. */
function expectedColor(mode: 'dark' | 'light', token: string): string {
  const diffClass = TOKEN_DIFF_CLASS[token];
  expect(diffClass, `nothing maps the ${token} token to a palette row`).toBeTruthy();
  const style = TOKEN_STYLES.find((s) => s.diffClass === diffClass);
  expect(style, `the ${token} token maps to ${diffClass}, which the table has no row for`).toBeTruthy();
  const hex = (mode === 'dark' ? style!.dark : style!.light).replace('#', '');
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16));
  return `rgb(${r}, ${g}, ${b})`;
}

/** The color the mounted theme paints `text` with, read off the DOM like a user sees it. */
function colorOf(view: EditorView, text: string): string {
  const span = Array.from(view.dom.querySelectorAll('span')).find((el) => el.textContent === text);
  if (!span) throw new Error(`no span for ${JSON.stringify(text)} — the grammar did not tokenize it`);
  return getComputedStyle(span).color;
}

describe('every token the engine emits gets painted', () => {
  it('the fixture really emits every token kind TOK names', () => {
    const emitted = new Set(leaves().map((l) => l.token));
    for (const kind of Object.values(TOK)) {
      expect(emitted, `the fixture never produces a ${kind} token`).toContain(kind);
    }
  });

  for (const mode of ['dark', 'light'] as const) {
    it(`the ${mode} palette paints each token the color its row declares`, () => {
      const view = editorFor(mode);
      for (const { token, text } of leaves()) {
        // Brackets are painted by bracket-pairs.ts (a nesting level), not by the token palette.
        if (text === '(' || text === ')') continue;
        expect(colorOf(view, text), `${token} (${JSON.stringify(text)})`).toBe(expectedColor(mode, token));
      }
    });
  }

  it('paints a declared constant as a constant, not as a keyword', () => {
    // The regression: none of the three old lists had an `atom` row, and `atom` resolves through
    // `keyword`, so True/False/None/nil took the keyword color and read as control flow.
    const view = editorFor('dark');
    expect(leaves().find((l) => l.text === 'con')?.token).toBe(TOK.constant);
    expect(colorOf(view, 'con')).toBe(expectedColor('dark', TOK.constant));
    expect(colorOf(view, 'con')).not.toBe(expectedColor('dark', TOK.keyword));
  });

  it('the diff highlighter gives every one of them a class', () => {
    const parsed = buildLanguageSupport(EVERY_TOKEN, 'fixture').language.parser.parse(SAMPLE);
    const painted = new Map<string, string>();
    highlightTree(parsed, diffHighlighter, (from, to, classes) => {
      const text = SAMPLE.slice(from, to);
      if (text.trim() && classes) painted.set(text, classes);
    });
    for (const { token, text } of leaves()) {
      if (text === '(' || text === ')') continue;
      expect(painted.get(text), `${token} (${JSON.stringify(text)}) gets no diff class`).toBeTruthy();
    }
  });
  it('paints a JSON literal as a constant, off the real core grammar', () => {
    // The same mislabelling one grammar over: `@codemirror/lang-json` tags its literals `bool` and
    // `null`, neither of which is `atom`, and the table had no row for either — so in a `.json` diff
    // `true` came out unpainted and `null` came out the color of `if`. No profile involved: JSON is a
    // core fallback, so this is what a user with no extension installed sees.
    const doc = '{"a": true, "b": false, "c": null, "d": 1}';
    const painted = new Map<string, string>();
    highlightTree(json().language.parser.parse(doc), diffHighlighter, (from, to, classes) => {
      if (classes) painted.set(doc.slice(from, to), classes);
    });
    for (const literal of ['true', 'false', 'null']) {
      expect(painted.get(literal), `${literal} is not painted`).toBe('hl-atom');
    }
    expect(painted.get('1')).toBe('hl-number');
  });
});

describe('one stylesheet paints every token class', () => {
  // The fourth leg, and the drift this exists for: the commit overlay used to carry its own copy of the
  // class list (still missing the constant token after the table gained one) while the diff view
  // carried a second copy. One file now — so these check the table against that file in both
  // directions, and against every other stylesheet not naming a token at all.
  const TOKEN_COLOURS = join('styles', 'token-colors.css');
  const ruleLists = new Map<string, Set<string>>(
    readdirSync(SRC, { recursive: true })
      .filter((f): f is string => typeof f === 'string' && /\.(css|svelte)$/.test(f) && !f.includes('__tests__'))
      .map((f) => [
        f,
        new Set([...readFileSync(join(SRC, f), 'utf8').matchAll(/\.hl-[a-z0-9-]+/g)].map((m) => m[0].slice(1))),
      ]),
  );
  const rules = ruleLists.get(TOKEN_COLOURS);

  it('the shared stylesheet exists and is the only file naming a token class', () => {
    expect(rules, `${TOKEN_COLOURS} is not readable`).toBeTruthy();
    for (const [file, found] of ruleLists) {
      if (file === TOKEN_COLOURS) continue;
      expect([...found], `${file} colors a token class a second time`).toEqual([]);
    }
  });

  it('every token class in the table has a rule', () => {
    for (const style of TOKEN_STYLES) {
      expect(rules, `${style.diffClass} (tag: ${String(style.tag)}) has no rule in ${TOKEN_COLOURS}`).toContain(
        style.diffClass,
      );
    }
  });

  it('every rule in the stylesheet belongs to a token or to bracket levels', () => {
    const bracketLevels = new Set(['hl-bracket-l0', 'hl-bracket-l1', 'hl-bracket-l2']);
    const known = new Set(TOKEN_STYLES.map((s) => s.diffClass));
    for (const rule of rules!) {
      expect(known.has(rule) || bracketLevels.has(rule), `${rule} is styled but nothing emits it`).toBe(true);
    }
  });
});

