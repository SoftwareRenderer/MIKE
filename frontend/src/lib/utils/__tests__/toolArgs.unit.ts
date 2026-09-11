import { describe, it, expect } from 'vitest';
import { formatToolArgs, normalizeEdits, rawArgsComplete, streamedPath } from '$lib/utils/toolArgs';

describe('normalizeEdits', () => {
    it('passes well-formed edits through', () => {
        expect(normalizeEdits([{ oldText: 'a', newText: 'b' }])).toEqual([{ oldText: 'a', newText: 'b' }]);
    });

    it('defaults a missing oldText to empty string (pure insertion)', () => {
        expect(normalizeEdits([{ newText: 'b' }])).toEqual([{ oldText: '', newText: 'b' }]);
    });

    it('defaults a missing newText to empty string (pure deletion)', () => {
        expect(normalizeEdits([{ oldText: 'a' }])).toEqual([{ oldText: 'a', newText: '' }]);
    });

    it('coerces non-string sides to empty string instead of leaving undefined', () => {
        const out = normalizeEdits([{ oldText: 42, newText: 'x' }, { oldText: 'y', newText: {} }]);
        expect(out).toEqual([{ oldText: '', newText: 'x' }, { oldText: 'y', newText: '' }]);
        for (const e of out) {
            expect(typeof e.oldText).toBe('string');
            expect(typeof e.newText).toBe('string');
        }
    });

    it('drops entries with no string side at all', () => {
        expect(normalizeEdits([{ oldText: 42, newText: null }])).toEqual([]);
    });

    it('drops junk entries alongside valid ones', () => {
        expect(normalizeEdits([null, undefined, 'nope', 7, [], [{ oldText: 'a', newText: 'b' }], { oldText: 'c', newText: 'd' }]))
            .toEqual([{ oldText: 'c', newText: 'd' }]);
    });

    it('drops entries with neither side renderable (e.g. hallucinated keys)', () => {
        expect(normalizeEdits([{ old_text: 'a', new_text: 'b' }, { oldText: 'a', newText: 'b' }]))
            .toEqual([{ oldText: 'a', newText: 'b' }]);
    });

    it('returns [] for non-array input', () => {
        expect(normalizeEdits(undefined)).toEqual([]);
        expect(normalizeEdits(null)).toEqual([]);
        expect(normalizeEdits('oldText')).toEqual([]);
        expect(normalizeEdits({ oldText: 'a', newText: 'b' })).toEqual([]);
    });
});

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

    it('counts an edit whose oldText side is missing (partial streaming args)', () => {
        expect(formatToolArgs('edit_file', { path: 'file.go', edits: [{ newText: 'b' }] })).toBe('./file.go (1 edit)');
    });

    it('does not count unrenderable edits', () => {
        expect(formatToolArgs('edit_file', { path: 'file.go', edits: [{ old_text: 'a' }, null] })).toBe('./file.go');
    });

    it('does not throw when edits is not an array', () => {
        expect(formatToolArgs('edit_file', { path: 'file.go', edits: 'nope' })).toBe('./file.go');
        expect(formatToolArgs('edit_file', { path: 'file.go', edits: {} })).toBe('./file.go');
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

    it('does not use a non-string path as a display string', () => {
        expect(formatToolArgs('write_file', { path: { named: 'x' } })).toBe('');
    });
});

describe('streamedPath', () => {
    it('takes the path the tool event carries first', () => {
        expect(streamedPath({ path: 'args.go' }, '{"path":"raw.go"}', 'event.go')).toBe('event.go');
    });

    it('reads a path out of arguments that are still streaming', () => {
        expect(streamedPath({}, '{"path":"src/file.go","content":"pack', '')).toBe('src/file.go');
    });

    it('unescapes the path read out of streaming arguments', () => {
        expect(streamedPath({}, '{"path":"a \\"quoted\\" name.go","content":', '')).toBe('a "quoted" name.go');
    });

    it('answers empty when no source names a file', () => {
        expect(streamedPath({}, '{"content":"pack')).toBe('');
        expect(streamedPath(null)).toBe('');
    });

    it('does not use a non-string path', () => {
        expect(streamedPath({ path: 7 })).toBe('');
    });
});

describe('rawArgsComplete', () => {
    it('is false while the arguments are still arriving', () => {
        expect(rawArgsComplete('{"path":"a.go","content":"hel')).toBe(false);
        expect(rawArgsComplete(undefined)).toBe(false);
    });

    it('is true once the arguments are the whole object', () => {
        expect(rawArgsComplete('{"path":"a.go","content":"x"}')).toBe(true);
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
