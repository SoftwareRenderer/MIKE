import { describe, it, expect } from 'vitest';
import { identityKey, slugifyAuthor } from '../extension-identity';

describe('extension identity key', () => {
  it('aligns a registry display author with its on-disk namespace dir', () => {
    expect(identityKey('Go Team', 'gopls')).toBe('go-team/gopls');
    expect(identityKey('microsoft', 'typescript-language-server')).toBe(
      identityKey('Microsoft', 'typescript-language-server'),
    );
  });

  it('slugifies: collapses separators, trims trailing ones, keeps _ and .', () => {
    expect(slugifyAuthor('  Rust Analyzer  Project ')).toBe('rust-analyzer-project');
    expect(slugifyAuthor('a..b')).toBe('a..b');
    expect(slugifyAuthor('trailing-')).toBe('trailing');
  });

  it('mirrors the backend authorDir() quirks exactly, blank included', () => {
    // backend/extensions/handler.go authorDir(): blank -> "unknown"; a symbol-only
    // author collapses to "-" and is then TrimRight'ed to "" — the empty segment is
    // a quirk of BOTH implementations, and the frontend must reproduce it, because
    // an identity mismatch is what makes one extension count as two. If you change
    // one side, change the other and this test.
    expect(slugifyAuthor(undefined)).toBe('unknown');
    expect(slugifyAuthor('   ')).toBe('unknown');
    expect(slugifyAuthor('!!!')).toBe('');
  });

  it('does NOT merge two different extensions', () => {
    expect(identityKey('softwarerenderer', 'html-preview')).not.toBe(
      identityKey('softwarerenderer', 'markdown-preview'),
    );
  });
});
