/**
 * The bundled manifests, read from the sibling registry checkout and run through the real engine.
 *
 * This is the check that the mechanism and the shipped data actually agree: a profile in a unit-test
 * fixture proves the engine can color *something*, and only the manifest an install really gets proves
 * it colors Go. The last case matters most — an extension installed before `capitalized_types` and
 * `call_as_function` existed carries neither key, and it must highlight anyway, because a fix nobody
 * reinstalled has not happened.
 */
import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync } from 'fs';
import { join } from 'path';
import { EditorState } from '@codemirror/state';
import type { SyntaxNode } from '@lezer/common';
import { ensureSyntaxTree, syntaxTree } from '@codemirror/language';
import { buildLanguageSupport } from '../spec-language';
import { bracketPairLevels } from '../bracket-pairs';
import type { LSPConfig, SyntaxSpec } from '$lib/types';

/** The registry source beside this repo, or null when it is not checked out. */
function lspManifest(name: string): LSPConfig | null {
  // vitest runs with cwd=frontend, so the sibling checkout is two levels up. One level up lands
  // inside this repo, where MIKE-extensions is never checked out — and a path that never exists is a
  // guard that skips its whole suite in silence, reporting a green run while measuring nothing.
  const root = [join(process.cwd(), '..', '..'), join(process.cwd(), '..')]
    .map((dir) => join(dir, 'MIKE-extensions'))
    .find((dir) => existsSync(join(dir, 'lsp')));
  if (!root) return null;
  const path = join(root, 'lsp', name, 'manifest.json');
  if (!existsSync(path)) {
    throw new Error(`${path} is not readable — this suite is the check that the shipped manifests work
      against the engine, so a missing one fails rather than skips`);
  }
  const manifest = JSON.parse(readFileSync(path, 'utf8'));
  return (manifest.capabilities?.lsp as LSPConfig) ?? null;
}

/** The declared profile for one file extension, resolved the way the editor resolves it. */
function profileFor(lsp: LSPConfig, extension: string): SyntaxSpec {
  const fileType = (lsp.file_types || []).find((ft) => ft.extension === extension);
  const spec = fileType?.syntax ? lsp.syntax?.[fileType.syntax] : undefined;
  if (!spec) throw new Error(`no syntax profile for .${extension} in ${lsp.command}`);
  return spec;
}

/** Every styled range as `[text, tag]`. */
function tokens(spec: SyntaxSpec, doc: string): Array<[string, string]> {
  const state = EditorState.create({ doc, extensions: [buildLanguageSupport(spec, 'bundled')] });
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

function levels(spec: SyntaxSpec, doc: string): number[] {
  const state = EditorState.create({ doc, extensions: [buildLanguageSupport(spec, 'bundled')] });
  return bracketPairLevels(state, spec).map((b) => b.level);
}

const REPORTED_LINE = 'func formatChatHistory(messages []types.ChatMessage, skipSummaryMsg bool) string {';

describe('the bundled gopls manifest highlights Go', () => {
  const lsp = lspManifest('gopls');
  const go = lsp ? profileFor(lsp, 'go') : null;

  it.skipIf(!go)('colors the line from the bug report, not just its keywords', () => {
    const found = new Map(tokens(go!, REPORTED_LINE));
    expect(found.get('func')).toBe('keyword');
    expect(found.get('formatChatHistory')).toBe('variableName.function');
    expect(found.get('ChatMessage')).toBe('typeName');
    expect(found.get('bool')).toBe('typeName');
    expect(found.get('string')).toBe('typeName');
  });

  it.skipIf(!go)('gives each bracket its nesting level', () => {
    // ( [ ] ) and the block brace: the slice brackets are the only pair one level deeper.
    expect(levels(go!, 'func formatChatHistory(messages []types.ChatMessage) string {')).toEqual([
      0, 1, 1, 0, 0,
    ]);
  });

  it.skipIf(!go)('declares its conventions instead of relying on the defaults', () => {
    expect(go!.capitalized_types).toBe(true);
    expect(go!.call_as_function).toBe(true);
    expect(go!.pairs?.length).toBeGreaterThan(0);
  });

  it.skipIf(!go)('still colors a manifest installed before these fields existed', () => {
    // Exactly what a long-installed extension sends: the same profile, with neither key present.
    const { capitalized_types, call_as_function, ...installed } = go!;
    expect(capitalized_types).toBe(true); // so the spread below is provably what removed them
    expect(call_as_function).toBe(true);

    const found = new Map(tokens(installed, REPORTED_LINE));
    expect(found.get('formatChatHistory')).toBe('variableName.function');
    expect(found.get('ChatMessage')).toBe('typeName');
    expect(found.get('func')).toBe('keyword');
    expect(levels(installed, 'f(a []int) {')).toEqual([0, 1, 1, 0, 0]);
  });
});

// The languages whose profiles a user can get wrong by shipping nothing: one declared profile each,
// and a profile that declares nothing at all paints the file as plain text.
describe('the bundled manifests color Python, TypeScript and JavaScript', () => {
  const pyright = lspManifest('pyright');
  const tsserver = lspManifest('typescript-language-server');
  const python = pyright ? profileFor(pyright, 'py') : null;
  const typescript = tsserver ? profileFor(tsserver, 'ts') : null;
  const javascript = tsserver ? profileFor(tsserver, 'js') : null;

  it.skipIf(!python)('paints Python keywords, types, strings and comments', () => {
    const found = new Map(tokens(python!, 'def greet(name: str) -> str:\n    return "hi"  # salutation\n'));
    expect(found.get('def')).toBe('keyword');
    expect(found.get('str')).toBe('typeName');
    expect(found.get('"hi"')).toBe('string');
    expect(found.get('# salutation')).toBe('comment');
    expect(levels(python!, 'f(a[0]) {')).toEqual([0, 1, 1, 0, 0]);
  });

  it.skipIf(!typescript)('paints TypeScript what JavaScript has no word for', () => {
    const found = new Map(tokens(typescript!, 'export interface Box { x: number }\nconst s = `{`; // {\n'));
    expect(found.get('export')).toBe('keyword');
    expect(found.get('interface')).toBe('keyword');
    expect(found.get('number')).toBe('typeName');
    // A brace inside a string and inside a comment opens nothing — the fold and the color agree.
    expect(found.get('`{`')).toBe('string');
    expect(found.get('// {')).toBe('comment');
  });

  it.skipIf(!javascript)('paints JavaScript through its own profile, not TypeScript\'s', () => {
    const found = new Map(tokens(javascript!, 'export const twice = (a) => String(a); // hi\n'));
    expect(found.get('export')).toBe('keyword');
    expect(found.get('const')).toBe('keyword');
    expect(found.get('String')).toBe('className'); // a declared builtin
    expect(found.get('// hi')).toBe('comment');
    // The two profiles are different objects: `.js` must not be handed the TypeScript vocabulary
    // (its keyword list has `interface`, which is not a JavaScript keyword).
    expect(javascript).not.toBe(typescript);
    expect(javascript!.keywords).not.toContain('interface');
  });
});
