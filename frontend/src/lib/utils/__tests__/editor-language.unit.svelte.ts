import { describe, it, expect, beforeEach } from 'vitest';
import { EditorState } from '@codemirror/state';
import type { SyntaxNode } from '@lezer/common';
import { ensureSyntaxTree, syntaxTree } from '@codemirror/language';
import { getGrammarForFence, getLanguageSupport, resolveFile } from '$lib/utils/editor-language';
import { fallbackLanguage } from '$lib/utils/fallback-languages';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension, LSPConfig, SyntaxSpec } from '$lib/types';

/**
 * Which grammar loads for a file and which languageId reaches the server — both answered per file
 * type by the manifests, since `ts`, `tsx`, `js` and `jsx` share one routing language but need four
 * wire ids and two labels.
 */

const TS_PROFILE: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [{ quote: "'", escapes: true }, { quote: '"', escapes: true }],
  pairs: [{ open: '{', close: '}' }, { open: '(', close: ')' }],
  keywords: ['interface', 'type', 'function', 'const', 'export'],
  numbers: true,
};

function addMockLSPExt(name: string, lsp: LSPConfig, enabled = true): Extension {
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
      capabilities: { lsp },
    },
    config: {},
    container_config: {},
    sandbox_config: { network_isolation: true, memory_limit: 256 },
    install_source: 'manual',
    enabled,
    running: false,
    installed_at: new Date().toISOString(),
    built_at: new Date().toISOString(),
  };
  extensionManager.state.extensions.push(ext);
  return ext;
}

/** The published typescript-language-server declaration, in miniature. */
function addTypeScriptExt() {
  return addMockLSPExt('typescript-language-server', {
    command: 'typescript-language-server',
    args: ['--stdio'],
    language: 'typescript',
    file_types: [
      { extension: 'ts', language_id: 'typescript', label: 'TypeScript', syntax: 'typescript' },
      { extension: 'tsx', language_id: 'typescriptreact', label: 'TypeScript JSX', syntax: 'typescript' },
      { extension: 'js', language_id: 'javascript', label: 'JavaScript', syntax: 'javascript' },
      { extension: 'jsx', language_id: 'javascriptreact', label: 'JavaScript JSX', syntax: 'javascript' },
    ],
    syntax: { typescript: TS_PROFILE, javascript: TS_PROFILE },
  });
}

/** The name of the core fallback grammar for a path. */
function fallbackName(filePath: string): string | undefined {
  return fallbackLanguage(filePath)?.language.name;
}

/** Tagged tokens a grammar produces, to assert a declared profile really ran. */
function tagged(filePath: string, source: string): Array<[string, string]> {
  const support = getLanguageSupport(filePath);
  if (!support) return [];
  const state = EditorState.create({ doc: source, extensions: [support] });
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

beforeEach(() => {
  extensionManager.state.extensions = [];
});

describe('resolveFile — the wire id and the label', () => {
  it('gives each JavaScript/TypeScript file its own id and label', () => {
    addTypeScriptExt();
    expect(resolveFile('src/a.ts')).toEqual({
      languageId: 'typescript',
      label: 'TypeScript',
      spec: TS_PROFILE,
      language: 'typescript',
    });
    expect(resolveFile('src/App.tsx').languageId).toBe('typescriptreact');
    expect(resolveFile('src/App.tsx').label).toBe('TypeScript JSX');
    expect(resolveFile('src/app.js').languageId).toBe('javascript');
    expect(resolveFile('src/app.jsx').languageId).toBe('javascriptreact');
  });

  it('does not collapse the JS family onto the routing language', () => {
    addTypeScriptExt();
    // Routing takes the manifest language — one server process owns the whole JS family — while the
    // wire id stays per-file, because it decides how the buffer is parsed.
    expect(resolveFile('App.tsx').language).toBe('typescript');
    expect(resolveFile('App.tsx').languageId).toBe('typescriptreact');
  });

  it('answers nothing for a file no extension declares', () => {
    addTypeScriptExt();
    const resolved = resolveFile('notes.txt');
    expect(resolved.languageId).toBe('');
    expect(resolved.label).toBe('');
    expect(resolved.spec).toBeNull();
    // Nothing owns the file, so nothing claims a language for it.
    expect(resolved.language).toBe('');
  });

  it('ignores an extension whose editor features are off', () => {
    addMockLSPExt('typescript-language-server', {
      ...addTypeScriptConfigOnly(),
      editor_enabled: false,
    });
    // Not owned for editing: no grammar of its own, no wire id offered.
    expect(resolveFile('a.ts').languageId).toBe('');
    expect(resolveFile('a.ts').spec).toBeNull();
  });

  it('matches a suffixless file named after the language', () => {
    addMockLSPExt('gopls', {
      command: 'gopls',
      args: [],
      language: 'go',
      file_types: [{ extension: 'go', language_id: 'go', label: 'Go' }],
    });
    expect(resolveFile('repo/go').languageId).toBe('go');
    expect(resolveFile('repo/go').label).toBe('Go');
  });
});

function addTypeScriptConfigOnly(): LSPConfig {
  return {
    command: 'typescript-language-server',
    args: [],
    language: 'typescript',
    file_types: [{ extension: 'ts', language_id: 'typescript', label: 'TypeScript', syntax: 'typescript' }],
    syntax: { typescript: TS_PROFILE },
  };
}

describe('getLanguageSupport — declared profile first, fallback second', () => {
  it('builds a grammar out of the declared profile', () => {
    addTypeScriptExt();
    const tokens = tagged('a.ts', "interface Box { x: number } // what\nconst s = '{';");
    // The word from the original bug report is a keyword because the manifest said so.
    expect(tokens.some(([text, name]) => text === 'interface' && name === 'keyword')).toBe(true);
    expect(tokens.some(([text, name]) => text === '// what')).toBe(true);
    expect(tokens.some(([text, name]) => text === "'{'")).toBe(true);
  });

  it('prefers an extension profile over the core fallback for the same suffix', () => {
    addMockLSPExt('json-lsp', {
      command: 'json-lsp',
      args: [],
      language: 'json',
      file_types: [{ extension: 'json', language_id: 'json', label: 'JSON', syntax: 'json' }],
      syntax: { json: { line_comment: '//', strings: [{ quote: '"', escapes: true }], numbers: true, pairs: [{ open: '{', close: '}' }] } },
    });
    // The core JSON grammar is a real parser named "JSON"; a declared profile is named for what it
    // is, which is how a caller can tell the two paths apart.
    expect(getLanguageSupport('cfg.json')?.language.name).toBe('mike-declared');
  });

  it('uses the core fallback when nothing declares the file', () => {
    // Compared against the fallback module itself, not against a name: the grammar's own casing is
    // its business, and this test is about *which* grammar loads.
    for (const path of ['cfg.json', 'README.md', 'index.html', 'app.css', 'ci.yml', 'q.sql', 't.tmpl']) {
      expect(getLanguageSupport(path)?.language.name, path).toBe(fallbackName(path));
    }
  });

  it('opens a declared file type whose profile is missing, without crashing', () => {
    addMockLSPExt('json-lsp', {
      command: 'json-lsp',
      args: [],
      language: 'json',
      file_types: [{ extension: 'json', language_id: 'json', label: 'JSON', syntax: 'absent-profile' }],
      syntax: {},
    });
    // The wire id is still right — a packaging gap in the highlighting must not corrupt the
    // protocol — and the grammar falls through to the core's JSON parser.
    expect(resolveFile('cfg.json').languageId).toBe('json');
    expect(resolveFile('cfg.json').spec).toBeNull();
    expect(getLanguageSupport('cfg.json')?.language.name).toBe(fallbackName('cfg.json'));
  });

  it('gives a declared language a name that is deliberately not a language id', () => {
    addTypeScriptExt();
    // The client falls back to Language.name when no id is passed — the bug this layer exists for,
    // so a declared grammar answers with a name no server can mistake for a language.
    expect(getLanguageSupport('a.ts')?.language.name).toBe('mike-declared');
    expect(resolveFile('a.ts').languageId).toBe('typescript');
  });

  it('returns null for extensionless build files and unknown types', () => {
    expect(getLanguageSupport('repo/Dockerfile')).toBeNull();
    expect(getLanguageSupport('repo/Makefile')).toBeNull();
    expect(getLanguageSupport('repo/CMakeLists.txt')).toBeNull();
    expect(getLanguageSupport('data.parquet')).toBeNull();
  });
});

// ─── getGrammarForFence — the language tag in a language server's markdown ───────────────────

const GO_PROFILE: SyntaxSpec = {
  line_comment: '//',
  block_comment: { open: '/*', close: '*/' },
  strings: [{ quote: '"', escapes: true }, { quote: '`', escapes: false }],
  pairs: [{ open: '{', close: '}' }, { open: '(', close: ')' }],
  keywords: ['func', 'package', 'return', 'type', 'chan'],
  types: ['int', 'string', 'bool'],
  numbers: true,
};

function addGoExt(): Extension {
  return addMockLSPExt('gopls', {
    command: 'gopls',
    args: [],
    language: 'go',
    file_types: [{ extension: 'go', language_id: 'go', label: 'Go', syntax: 'go' }],
    syntax: { go: GO_PROFILE },
  });
}

describe('getGrammarForFence — which grammar paints a tooltip\'s code block', () => {
  it('resolves the tag the server actually writes', () => {
    addGoExt();
    // Any of the three declared fields answers the tag gopls writes: ```go.
    expect(getGrammarForFence('go', 'repo/x.go')).toBe(getLanguageSupport('repo/x.go')?.language);
    addTypeScriptExt();
    expect(getGrammarForFence('typescript', 'repo/x.go')).toBe(getLanguageSupport('a.ts')?.language);
    // By `extension`, for the tags that are suffixes rather than ids — and tsserver writes both.
    expect(getGrammarForFence('tsx', 'repo/x.go')).toBe(getLanguageSupport('a.tsx')?.language);
    expect(getGrammarForFence('js', 'repo/x.go')).toBe(getLanguageSupport('a.js')?.language);
  });

  it('ignores the tag\'s case and padding', () => {
    addGoExt();
    const go = getLanguageSupport('repo/x.go')?.language;
    expect(getGrammarForFence('Go', 'repo/x.go')).toBe(go);
    expect(getGrammarForFence('  GO  ', 'repo/x.go')).toBe(go);
  });

  it('takes a declared profile over the core fallback for the same tag', () => {
    addMockLSPExt('json-lsp', {
      command: 'json-lsp',
      args: [],
      language: 'json',
      file_types: [{ extension: 'json', language_id: 'json', label: 'JSON', syntax: 'json' }],
      syntax: { json: { line_comment: '//', strings: [{ quote: '"', escapes: true }], numbers: true, pairs: [{ open: '{', close: '}' }] } },
    });
    expect(getGrammarForFence('json', 'notes.txt')?.name).toBe('mike-declared');
  });

  it('falls back to the core grammar for a tag nothing declares but the core knows', () => {
    // json is structural, so the core table owns it.
    expect(getGrammarForFence('json', 'repo/x.go')?.name).toBe(fallbackName('x.json'));
    expect(getGrammarForFence('yaml', 'repo/x.go')?.name).toBe(fallbackName('x.yaml'));
  });

  it('paints an unlabelled fence as the file the documentation is about', () => {
    addGoExt();
    const go = getLanguageSupport('repo/x.go')?.language;
    // A doc comment's indented block has no fence tag.
    expect(getGrammarForFence('', 'repo/x.go')).toBe(go);
    expect(getGrammarForFence('   ', 'repo/x.go')).toBe(go);
    // A language nothing declares uses the file's grammar: the agreed fallback, not a gap.
    expect(getGrammarForFence('bash', 'repo/x.go')).toBe(go);
    expect(getGrammarForFence('golang', 'repo/x.go')).toBe(go);
  });

  it('leaves a fence that declares itself prose unhighlighted', () => {
    addGoExt();
    // Checked before the file's grammar: `text` means do not tokenize.
    for (const tag of ['text', 'txt', 'plain', 'plaintext']) {
      expect(getGrammarForFence(tag, 'repo/x.go'), tag).toBeNull();
    }
  });

  it('answers null when neither the tag nor the file has a grammar', () => {
    addGoExt();
    expect(getGrammarForFence('bash', 'notes.txt')).toBeNull();
    expect(getGrammarForFence('', 'repo/Makefile')).toBeNull();
    expect(getGrammarForFence('', 'repo/Dockerfile')).toBeNull();
  });

  it('tokenizes the signature with the grammar it resolved', () => {
    addGoExt();
    const lang = getGrammarForFence('go', 'repo/x.go');
    expect(lang).not.toBeNull();
    const state = EditorState.create({ doc: 'func Println(a ...any) (n int, err error)', extensions: [lang!] });
    ensureSyntaxTree(state, state.doc.length, 50);
    const keywords: string[] = [];
    syntaxTree(state).iterate({
      enter: (node) => {
        if (node.name === 'keyword') keywords.push(state.sliceDoc(node.from, node.to));
      },
    });
    expect(keywords).toContain('func');
  });
});

