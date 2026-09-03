/**
 * The bundled manifests, read from the sibling registry checkout and run through the real engine.
 *
 * This is the check that the mechanism and the shipped data actually agree: a profile in a unit-test
 * fixture proves the engine can colour *something*, and only the manifest an install really gets proves
 * it colours Go. The last case matters most — an extension installed before `capitalized_types` and
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
  const path = join(process.cwd(), '..', 'MIKE-extensions', 'lsp', name, 'manifest.json');
  if (!existsSync(path)) return null;
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

  it.skipIf(!go)('colours the line from the bug report, not just its keywords', () => {
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

  it.skipIf(!go)('still colours a manifest installed before these fields existed', () => {
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
