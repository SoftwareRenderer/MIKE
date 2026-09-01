import { describe, it, expect, beforeEach } from 'vitest';
import { EditorState } from '@codemirror/state';
import { syntaxTree } from '@codemirror/language';
import { getLanguageSupport, lspLanguageId } from '$lib/utils/editor-language';
import { detectLanguage } from '$lib/utils/lsp-connection-manager';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension } from '$lib/types';

/**
 * The editor asks two questions about a file and used to answer only one of them:
 * which CodeMirror grammar to load, and which languageId to hand the language
 * server. Getting the second wrong is what made tsserver parse a .ts file as
 * JavaScript and report "'…' declarations can only be used in TypeScript files."
 */

function addMockLSPExt(name: string, language: string, extensions: string[]) {
  const ext: Extension = {
    id: `${name}-1`,
    name,
    version: '1.0.0',
    type: 'lsp',
    manifest: {
      display_name: name,
      description: '',
      author: 'Test',
      version: '1.0.0',
      capabilities: {
        lsp: { command: name, args: [], extensions, language },
      },
    },
    config: {},
    container_config: {},
    sandbox_config: { network_isolation: true, memory_limit: 256 },
    install_source: 'manual',
    enabled: true,
    running: false,
    installed_at: new Date().toISOString(),
    built_at: new Date().toISOString(),
  };
  extensionManager.state.extensions.push(ext);
}

/** Count the parse-error nodes a grammar produces for a document. */
function errorNodes(filePath: string, source: string): number {
  const support = getLanguageSupport(filePath);
  const state = EditorState.create({ doc: source, extensions: support ? [support] : [] });
  let count = 0;
  syntaxTree(state).cursor().iterate((n) => {
    // Lezer records a recovered parse error as a "⚠" node.
    if (n.name === '⚠') count++;
  });
  return count;
}

const TS_SOURCE = [
  "export type HoverZone = 'token' | 'outside';",
  'export interface Box { x: number; y: number }',
  'export function inBox(p: { x: number }, b: Box): boolean { return p.x >= b.x }',
].join('\n');

describe('lspLanguageId', () => {
  beforeEach(() => {
    extensionManager.state.extensions = [];
  });

  it('gives every JavaScript/TypeScript file its own id', () => {
    expect(lspLanguageId('a.ts')).toBe('typescript');
    expect(lspLanguageId('a.mts')).toBe('typescript');
    expect(lspLanguageId('a.cts')).toBe('typescript');
    expect(lspLanguageId('a.tsx')).toBe('typescriptreact');
    expect(lspLanguageId('a.js')).toBe('javascript');
    expect(lspLanguageId('a.mjs')).toBe('javascript');
    expect(lspLanguageId('a.cjs')).toBe('javascript');
    expect(lspLanguageId('a.jsx')).toBe('javascriptreact');
  });

  it('does not collapse the JS family onto the manifest language', () => {
    addMockLSPExt('typescript-language-server', 'typescript', ['ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs']);
    // Routing still wants the manifest language…
    expect(detectLanguage('app.tsx')).toBe('typescript');
    // …but tsserver turns the wire id into a ScriptKind, so it must stay per-file:
    // a .tsx opened as "typescript" answers with 15 × "',' expected.".
    expect(lspLanguageId('app.tsx')).toBe('typescriptreact');
    expect(lspLanguageId('app.jsx')).toBe('javascriptreact');
    expect(lspLanguageId('app.js')).toBe('javascript');
  });

  it('uses the manifest language for every other language', () => {
    addMockLSPExt('gopls', 'go', ['go']);
    addMockLSPExt('rust-analyzer', 'rust', ['rs']);
    addMockLSPExt('svelte-language-server', 'svelte', ['svelte']);
    expect(lspLanguageId('main.go')).toBe('go');
    expect(lspLanguageId('lib.rs')).toBe('rust');
    // The Svelte grammar has an empty Language.name, so this one is load-bearing.
    expect(lspLanguageId('App.svelte')).toBe('svelte');
  });

  it('falls back to the extension when no LSP extension is installed', () => {
    expect(lspLanguageId('config.toml')).toBe('toml');
    expect(lspLanguageId('main.go')).toBe('go');
  });
});

describe('getLanguageSupport', () => {
  it('loads the TypeScript dialect for .ts and .tsx', () => {
    expect(getLanguageSupport('a.ts')?.language.name).toBe('typescript');
    expect(getLanguageSupport('a.tsx')?.language.name).toBe('typescript');
    expect(getLanguageSupport('a.js')?.language.name).toBe('javascript');
    expect(getLanguageSupport('a.jsx')?.language.name).toBe('javascript');
  });

  it('parses TypeScript syntax without error nodes', () => {
    // Meaningful only because the JavaScript grammar does fail on this source.
    expect(errorNodes('a.js', TS_SOURCE)).toBeGreaterThan(0);
    expect(errorNodes('a.ts', TS_SOURCE)).toBe(0);
    expect(errorNodes('a.tsx', TS_SOURCE)).toBe(0);
  });

  it('documents that the grammar name cannot express tsx or svelte', () => {
    // @codemirror/lsp-client falls back to Language.name when plugin() is not
    // given an id; that fallback is too coarse (tsx) and empty (svelte), which is
    // why FileEditor passes lspLanguageId() explicitly.
    expect(getLanguageSupport('a.tsx')?.language.name).toBe('typescript');
    expect(lspLanguageId('a.tsx')).toBe('typescriptreact');
    expect(getLanguageSupport('App.svelte')?.language.name).toBe('');
  });

  it('returns null for extensionless build files', () => {
    expect(getLanguageSupport('repo/Dockerfile')).toBeNull();
    expect(getLanguageSupport('repo/Makefile')).toBeNull();
    expect(getLanguageSupport('repo/CMakeLists.txt')).toBeNull();
  });
});
