/**
 * The engine's output — tokens, folds, indentation — from a fixture spec, including the refusal to
 * fold a bracket that lives inside a comment or a string.
 */
import { describe, it, expect } from 'vitest';
import { EditorState } from '@codemirror/state';
import type { SyntaxNode } from '@lezer/common';
import { IndentContext, ensureSyntaxTree, foldService, indentService, syntaxTree } from '@codemirror/language';
import { buildLanguageSupport, specBracketFoldService, specHasContent, specRegionFoldService } from '../spec-language';
import type { SyntaxSpec } from '$lib/types';

const SPEC: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [
    { quote: '"', escapes: true },
    { quote: '`', escapes: true, multiline: true },
  ],
  pairs: [
    { open: '{', close: '}' },
    { open: '(', close: ')' },
    { open: '[', close: ']' },
  ],
  keywords: ['function', 'const', 'return', 'if'],
  types: ['string', 'number'],
  constants: ['true', 'false', 'null'],
  builtins: ['console'],
  numbers: true,
  region: { open: '#region', close: '#endregion' },
};

function stateWith(doc: string, spec: SyntaxSpec = SPEC): EditorState {
  return EditorState.create({ doc, extensions: [buildLanguageSupport(spec, 'fixture')] });
}

/** Every styled range as `[text, tag]` — what a highlight style sees. */
function tokens(state: EditorState): Array<[string, string]> {
  ensureSyntaxTree(state, state.doc.length, 50);
  const out: Array<[string, string]> = [];
  const walk = (node: SyntaxNode) => {
    const first = node.firstChild;
    if (!first) {
      const text = state.sliceDoc(node.from, node.to);
      if (text.trim()) out.push([text, node.name]);
      return;
    }
    for (let child: SyntaxNode | null = first; child; child = child.nextSibling) walk(child);
  };
  walk(syntaxTree(state).topNode);
  return out;
}

/** What the fold services answer for a given line (1-based). */
function foldFor(state: EditorState, line: number): { from: number; to: number } | null {
  const row = state.doc.line(line);
  for (const provider of state.facet(foldService)) {
    const found = provider(state, row.from, row.to);
    if (found) return found;
  }
  return null;
}

describe('declared syntax: tokenizing', () => {
  it('tags keywords, strings, comments, numbers and constants from the spec', () => {
    const state = stateWith('function f(x: string) {\n  // note\n  const s = "hi {";\n  return 42;\n}');
    const found = new Map(tokens(state));

    expect(found.get('function')).toBe('keyword');
    expect(found.get('const')).toBe('keyword');
    expect(found.get('return')).toBe('keyword');
    expect(found.get('string')).toBe('typeName');
    expect(found.get('// note')).toBe('comment');
    expect(found.get('"hi {"')).toBe('string');
    expect(found.get('42')).toBe('number');
  });

  it('keeps a block comment open across lines and closes it', () => {
    const state = stateWith('/* one\ntwo */\nconst x = 1;');
    // A stream grammar tags one token per line, so the comment is the comment tokens joined —
    // what matters is that it covers the whole comment and stops there.
    const comment = tokens(state).filter(([, name]) => name === 'comment');
    expect(comment.map(([text]) => text).join('\n')).toBe('/* one\ntwo */');
    // The line after the closer is code again, not comment.
    expect(tokens(state).some(([text, name]) => text === 'const' && name === 'keyword')).toBe(true);
  });

  it('honours a multiline string and does not swallow the rest of the file', () => {
    const state = stateWith('const t = `a\nb`;\nconst done = true;');
    // The whole literal is string content — including the line it spans — and nothing after it is.
    // One token per line (that is what a stream grammar emits), so the join puts the newlines back.
    expect(tokens(state).filter(([, name]) => name === 'string').map(([text]) => text).join('\n')).toBe('`a\nb`');
    // constants are tagged with tags.atom, which is the node name the theme styles
    expect(tokens(state).some(([text, name]) => (text === 'true' && name === 'constant') || (text === 'true' && name === 'atom'))).toBe(true);
  });

  it('colours a called name and an upper-case name, which is what the word lists cannot know', () => {
    // The reported line, verbatim: before these rules it produced three styled ranges in all.
    const state = stateWith(
      'func formatChatHistory(messages []types.ChatMessage, skipSummaryMsg bool) string {',
      { ...SPEC, keywords: [...(SPEC.keywords || []), 'func'], types: [...(SPEC.types || []), 'bool'] },
    );
    const found = new Map(tokens(state));
    expect(found.get('func')).toBe('keyword');
    expect(found.get('formatChatHistory')).toBe('variableName.function');
    expect(found.get('ChatMessage')).toBe('typeName');
    expect(found.get('bool')).toBe('typeName');
    expect(found.get('string')).toBe('typeName');
    // A parameter is neither a call nor a type, and a package qualifier is not a type either.
    expect(found.has('messages')).toBe(false);
    expect(found.has('skipSummaryMsg')).toBe(false);
    expect(found.has('types')).toBe(false);
  });

  it('turns each shape rule off on its own, so an author is not stuck with a convention', () => {
    const doc = 'function plainThing Thing() {}';
    const noCalls = stateWith(doc, { ...SPEC, call_as_function: false });
    expect(new Map(tokens(noCalls)).has('Thing')).toBe(true); // upper-case still names a type
    expect(new Map(tokens(noCalls)).has('plainThing')).toBe(false);

    const noTypes = stateWith(doc, { ...SPEC, capitalized_types: false });
    expect(new Map(tokens(noTypes)).get('Thing')).toBe('variableName.function'); // the call still reads

    const neither = stateWith(doc, { ...SPEC, capitalized_types: false, call_as_function: false });
    const off = new Map(tokens(neither));
    expect(off.get('function')).toBe('keyword');
    // Both rules off is the old guarantee: a name in no word list is styled by nothing.
    expect(off.has('plainThing')).toBe(false);
    expect(off.has('Thing')).toBe(false);
  });

  it('does not read a name as a call because of a bracket further along the line', () => {
    const state = stateWith('plain := compute(a) // note (see docs)\nother, last = 1, 2 (x)');
    const found = new Map(tokens(state));
    expect(found.get('compute')).toBe('variableName.function');
    // `note` is followed by a comment whose bracket is not an argument list, and `last` by a comma.
    expect(found.has('note')).toBe(false);
    expect(found.has('last')).toBe(false);
  });

  it('prefers the repeated quote over the single quote it starts with', () => {
    const py: SyntaxSpec = {
      line_comment: '#',
      strings: [
        // Declared shortest-first on purpose: the engine matches longest-first, so the order an
        // author writes is not what decides the answer.
        { quote: '"', escapes: true },
        { quote: '"""', escapes: true, multiline: true },
      ],
      keywords: ['def'],
    };
    const state = stateWith('x = """a "quoted" b"""\ny = 1', py);
    const strings = tokens(state).filter(([, name]) => name === 'string');
    expect(strings.map(([text]) => text).join('')).toBe('"""a "quoted" b"""');
    // If the single quote had won, the text after it would have been read as code and produced
    // its own tokens; none of this line is code.
    expect(strings.length).toBeGreaterThan(0);
    expect(tokens(state).some(([text]) => text === 'quoted')).toBe(false);
  });

  it('keeps a multiline triple-quoted string across lines and closes it', () => {
    const py: SyntaxSpec = {
      line_comment: '#',
      strings: [{ quote: '"""', escapes: true, multiline: true }, { quote: '"', escapes: true }],
      keywords: ['def'],
      numbers: true,
    };
    const state = stateWith('x = """\nabc\ndef"""\nn = 42', py);
    expect(tokens(state).some(([text, name]) => text === '42' && name === 'number')).toBe(true);
    expect(tokens(state).filter(([, name]) => name === 'string').map(([t]) => t).join('\n')).toBe('"""\nabc\ndef"""');
  });

  it('honours a second line-comment marker', () => {
    const php: SyntaxSpec = { line_comment: '//', line_comments: ['#'], keywords: ['echo'] };
    const state = stateWith('// slash\n# hash\necho 1', php);
    const comments = tokens(state).filter(([, name]) => name === 'comment').map(([text]) => text);
    expect(comments).toEqual(['// slash', '# hash']);
  });

  it('does not let an unterminated single-line quote colour the rest of the file', () => {
    const state = stateWith('const s = "oops\nconst done = 1;');
    // The string dies at the newline, so the next line is tokenized as code again.
    expect(tokens(state).filter(([text, name]) => text === 'const' && name === 'keyword').length).toBe(2);
  });
});

describe('declared syntax: bracket folding', () => {
  it('folds the body of a block a line leaves open', () => {
    const state = stateWith('function f() {\n  return 1;\n}');
    const fold = foldFor(state, 1);
    expect(fold).not.toBeNull();
    // The body only: from the end of the opener's line to the start of the closer's line.
    expect(fold!.from).toBe(state.doc.line(1).to);
    expect(fold!.to).toBe(state.doc.line(3).from);
  });

  it('follows the buffer: an unbalanced block offers no fold, closing it offers one', () => {
    // `foldService` is the facet CodeMirror's folding (gutter, `foldAll`, `foldCode`) reads, so
    // this is the same question the fold arrow asks.
    const open = stateWith('function f() {\n  return 1;');
    expect(foldFor(open, 1)).toBeNull();
    const closed = open.update({ changes: { from: open.doc.length, insert: '\n}' } }).state;
    expect(foldFor(closed, 1)).not.toBeNull();
  });

  it('does not fold a bracket inside a line comment', () => {
    const state = stateWith('// if (x) {\nplain\nnext');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('does not fold a bracket inside a string', () => {
    const state = stateWith('const s = "{";\nplain\nnext');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('does not fold a bracket inside a block comment', () => {
    const state = stateWith('/* {\n */\nplain');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('does not fold a block that never closes', () => {
    const state = stateWith('function f() {\n  return 1;');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('does not fold a block that closes on the line it opened', () => {
    const state = stateWith('const a = { x: 1 };\nnext');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('folds nested blocks from the outermost opener on the line', () => {
    const state = stateWith('if (a) { for (b) {\n  x();\n}\n}');
    const fold = foldFor(state, 1);
    expect(fold!.to).toBe(state.doc.line(4).from);
  });
});

describe('declared syntax: region folding', () => {
  it('folds from a region marker to its end marker', () => {
    const state = stateWith('//#region part one\nconst a = 1;\n//#endregion\nconst b = 2;');
    const fold = foldFor(state, 1);
    expect(fold).not.toBeNull();
    expect(fold!.from).toBe(state.doc.line(1).to);
    expect(fold!.to).toBe(state.doc.line(3).from);
  });

  it('ignores a region marker that is only mentioned in prose', () => {
    const state = stateWith('// see the #region note below\nconst a = 1;\nconst b = 2;');
    expect(foldFor(state, 1)).toBeNull();
  });

  it('needs no region service when the spec declares none', () => {
    const { region, ...noRegion } = SPEC;
    expect(region).toBeTruthy();
    expect(noRegion).toBeTruthy();
    const state = EditorState.create({
      doc: '//#region x\ny\n//#endregion',
      extensions: [specRegionFoldService(noRegion), specBracketFoldService(noRegion)],
    });
    expect(state.facet(foldService).length).toBe(1); // only the bracket provider survives
  });
});

describe('declared syntax: indentation and language data', () => {
  it('indents one unit inside an unclosed opener', () => {
    const state = stateWith('function f() {\nif (a) {\n');
    const context = new IndentContext(state);
    for (const provider of state.facet(indentService)) {
      const indent = provider(context, state.doc.line(2).from);
      expect(indent).toBe(context.unit);
    }
  });

  it('dedents the line that closes a block', () => {
    const state = stateWith('function f() {\n  return 1;\n}');
    const context = new IndentContext(state);
    for (const provider of state.facet(indentService)) {
      expect(provider(context, state.doc.line(3).from)).toBe(0);
    }
  });

  it('publishes comment tokens and auto-close brackets from the same spec', () => {
    const state = stateWith('x');
    expect(state.languageDataAt('commentTokens', 0)).toEqual([{ line: '//', block: { open: '/*', close: '*/' } }]);
    expect(state.languageDataAt('closeBrackets', 0)).toEqual([{ brackets: ['{', '(', '['] }]);
  });

  // Nothing else is published as language data: no consumer reads a grammar's spec back out of a state.
});

describe('specHasContent', () => {
  it('treats an empty spec as no grammar at all', () => {
    expect(specHasContent({})).toBe(false);
    expect(specHasContent(undefined)).toBe(false);
    expect(specHasContent({ keywords: ['x'] })).toBe(true);
    expect(specHasContent({ pairs: [{ open: '{', close: '}' }] })).toBe(true);
  });
});
