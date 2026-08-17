import { describe, it, expect } from 'vitest';
import { formatToolArgs } from '$lib/utils/toolArgs';

describe('formatToolArgs - edit_file', () => {
    it('shows path from args when available', () => {
        const args = { path: 'src/file.go', edits: [{ oldText: 'a', newText: 'b' }] };
        expect(formatToolArgs('edit_file', args)).toBe('./src/file.go (1 edit)');
    });

    it('shows path from raw_args during streaming (empty args)', () => {
        // During streaming, args is {} but raw_args has the partial JSON
        const args = {};
        const rawArgs = '{"path":"src/file.go","edits":[{"oldText":"a","newText":"b"}]}';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./src/file.go (1 edit)');
    });

    it('shows path from partial raw_args during streaming', () => {
        // Partial JSON — path is complete but edits are still streaming
        const args = {};
        const rawArgs = '{"path":"src/file.go","edits":[{"oldText":"hel';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./src/file.go');
    });

    it('shows path from raw_args when args.path is empty but raw_args has it', () => {
        const args = { edits: [] };
        const rawArgs = '{"path":"backend/main.go","edits":[]}';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./backend/main.go');
    });

    it('returns empty string when neither args nor raw_args have path', () => {
        const args = {};
        expect(formatToolArgs('edit_file', args, '')).toBe('');
        expect(formatToolArgs('edit_file', args, undefined)).toBe('');
    });

    it('prefers args.path over raw_args when both present', () => {
        const args = { path: 'args/path.go', edits: [] };
        const rawArgs = '{"path":"raw/path.go","edits":[]}';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./args/path.go');
    });

    it('handles path already prefixed with ./', () => {
        const args = {};
        const rawArgs = '{"path":"./src/file.go","edits":[]}';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./src/file.go');
    });

    it('counts edits from raw_args when args.edits is empty', () => {
        const args = {};
        const rawArgs = '{"path":"file.go","edits":[{"oldText":"a","newText":"b"},{"oldText":"c","newText":"d"}]}';
        expect(formatToolArgs('edit_file', args, rawArgs)).toBe('./file.go (2 edits)');
    });
});

describe('formatToolArgs - bash', () => {
    it('shows command from args', () => {
        expect(formatToolArgs('bash', { command: 'ls -la' })).toBe('$ ls -la');
    });

    it('shows command from raw_args during streaming', () => {
        expect(formatToolArgs('bash', {}, '{"command":"ls -la"}')).toBe('$ ls -la');
    });

    it('shows partial command from streaming raw_args', () => {
        expect(formatToolArgs('bash', {}, '{"command":"apt ge')).toBe('$ apt ge');
    });

    it('shows building command placeholder when no args', () => {
        expect(formatToolArgs('bash', {})).toBe('$ (building command...)');
    });
});

describe('formatToolArgs - read_file', () => {
    it('shows path from args', () => {
        expect(formatToolArgs('read_file', { path: 'src/file.go' })).toBe('./src/file.go');
    });

    it('returns empty when no path', () => {
        expect(formatToolArgs('read_file', {})).toBe('');
    });
});

describe('formatToolArgs - write_file', () => {
    it('shows path from args', () => {
        expect(formatToolArgs('write_file', { path: 'src/file.go' })).toBe('./src/file.go');
    });

    it('returns empty when no path', () => {
        expect(formatToolArgs('write_file', {})).toBe('');
    });

    it('shows path from raw_args during streaming (empty args)', () => {
        const args = {};
        const rawArgs = '{"path":"src/file.go","content":"hello"}';
        expect(formatToolArgs('write_file', args, rawArgs)).toBe('./src/file.go');
    });

    it('shows path from partial raw_args during streaming', () => {
        const args = {};
        const rawArgs = '{"path":"src/file.go","content":"hel';
        expect(formatToolArgs('write_file', args, rawArgs)).toBe('./src/file.go');
    });

    it('prefers args.path over raw_args when both present', () => {
        const args = { path: 'args/path.go' };
        const rawArgs = '{"path":"raw/path.go"}';
        expect(formatToolArgs('write_file', args, rawArgs)).toBe('./args/path.go');
    });

    it('handles path already prefixed with ./', () => {
        const args = {};
        const rawArgs = '{"path":"./src/file.go"}';
        expect(formatToolArgs('write_file', args, rawArgs)).toBe('./src/file.go');
    });

    it('returns empty when neither args nor raw_args have path', () => {
        const args = {};
        expect(formatToolArgs('write_file', args, '')).toBe('');
        expect(formatToolArgs('write_file', args, undefined)).toBe('');
    });
});

describe('formatToolArgs - lsp_hover', () => {
    it('shows symbol and path', () => {
        expect(formatToolArgs('lsp_hover', { path: 'main.go', symbol: 'fmt' })).toBe('hover fmt in main.go');
    });

    it('shows fallback when no symbol', () => {
        expect(formatToolArgs('lsp_hover', { path: 'main.go' })).toBe('(hover in main.go)');
    });

    it('shows fallback when no path', () => {
        expect(formatToolArgs('lsp_hover', { symbol: 'fmt' })).toBe('hover fmt in file');
    });

    it('shows fallback when no symbol and no path', () => {
        expect(formatToolArgs('lsp_hover', {})).toBe('(hover in file)');
    });
});

describe('formatToolArgs - lsp_references', () => {
    it('shows symbol and path', () => {
        expect(formatToolArgs('lsp_references', { path: 'main.go', symbol: 'main' })).toBe('references for main in main.go');
    });

    it('shows fallback when no symbol', () => {
        expect(formatToolArgs('lsp_references', { path: 'main.go' })).toBe('(references in main.go)');
    });
});

describe('formatToolArgs - lsp_definition', () => {
    it('shows symbol and path', () => {
        expect(formatToolArgs('lsp_definition', { path: 'main.go', symbol: 'Println' })).toBe('definition of Println in main.go');
    });

    it('shows fallback when no symbol', () => {
        expect(formatToolArgs('lsp_definition', { path: 'main.go' })).toBe('(definition in main.go)');
    });
});

describe('formatToolArgs - removed lsp_code_actions tool', () => {
    it('falls back to the generic JSON summary', () => {
        expect(formatToolArgs('lsp_code_actions', { path: 'main.go', symbol: 'foo' })).toBe(
            JSON.stringify({ path: 'main.go', symbol: 'foo' }, null, 2)
        );
    });
});

describe('formatToolArgs - lsp_rename', () => {
    it('shows old and new symbol with path', () => {
        expect(formatToolArgs('lsp_rename', { path: 'main.go', symbol: 'foo', new_name: 'bar' })).toBe('rename foo to bar in main.go');
    });

    it('shows fallback when no new_name', () => {
        expect(formatToolArgs('lsp_rename', { path: 'main.go', symbol: 'foo' })).toBe('rename foo in main.go');
    });

    it('shows fallback when no symbol', () => {
        expect(formatToolArgs('lsp_rename', { path: 'main.go' })).toBe('(rename in main.go)');
    });

    it('shows fallback for empty args', () => {
        expect(formatToolArgs('lsp_rename', {})).toBe('(rename in file)');
    });
});
