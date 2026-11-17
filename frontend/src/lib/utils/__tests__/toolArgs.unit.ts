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

// One decision per row: which source names the file a card shows, and what the card prints when no
// source does. `read_file` reads only the finished arguments — its card has nothing to show until
// they arrive — while the two writing tools also read the body still streaming. The `./` root is
// `dotPath`'s, proved once per source rather than once per tool.
describe('formatToolArgs - the file a card names', () => {
    it.each([
        ['read_file from its args', 'read_file', { path: 'src/file.go' }, undefined, './src/file.go'],
        ['read_file with no path', 'read_file', {}, undefined, ''],
        ['read_file ignoring a path only the stream has', 'read_file', {}, '{"path":"src/file.go"}', ''],
        ['write_file from its args', 'write_file', { path: 'src/file.go' }, undefined, './src/file.go'],
        ['write_file from raw_args during streaming', 'write_file', {}, '{"path":"src/file.go","content":"hello"}', './src/file.go'],
        ['write_file from partial raw_args', 'write_file', {}, '{"path":"src/file.go","content":"hel', './src/file.go'],
        ['write_file preferring args.path over raw_args', 'write_file', { path: 'args/path.go' }, '{"path":"raw/path.go"}', './args/path.go'],
        ['write_file taking a path already rooted', 'write_file', {}, '{"path":"./src/file.go"}', './src/file.go'],
        ['write_file with no path anywhere', 'write_file', {}, undefined, ''],
        ['write_file refusing a path that is not text', 'write_file', { path: { named: 'x' } }, undefined, ''],
    ])('names %s', (_name, tool, args, rawArgs, expected) => {
        expect(formatToolArgs(tool, args, rawArgs)).toBe(expected);
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

// One decision: what an editor tool's card line says, and what it says when a part of it is
// missing. Each row is a phrase the catalog owns, and each fallback is a card that must still read
// as an action out of the editor.
describe('formatToolArgs - the editor tools', () => {
    it.each([
        ['lsp_hover with symbol and path', 'lsp_hover', { path: 'main.go', symbol: 'fmt' }, 'hover fmt in main.go'],
        ['lsp_hover with no symbol', 'lsp_hover', { path: 'main.go' }, '(hover in main.go)'],
        ['lsp_hover with no path', 'lsp_hover', { symbol: 'fmt' }, 'hover fmt in file'],
        ['lsp_hover with neither', 'lsp_hover', {}, '(hover in file)'],
        ['lsp_references with symbol and path', 'lsp_references', { path: 'main.go', symbol: 'main' }, 'references for main in main.go'],
        ['lsp_references with no symbol', 'lsp_references', { path: 'main.go' }, '(references in main.go)'],
        ['lsp_definition with symbol and path', 'lsp_definition', { path: 'main.go', symbol: 'Println' }, 'definition of Println in main.go'],
        ['lsp_definition with no symbol', 'lsp_definition', { path: 'main.go' }, '(definition in main.go)'],
        ['lsp_rename with both names', 'lsp_rename', { path: 'main.go', symbol: 'foo', new_name: 'bar' }, 'rename foo to bar in main.go'],
        ['lsp_rename with no new_name', 'lsp_rename', { path: 'main.go', symbol: 'foo' }, 'rename foo in main.go'],
        ['lsp_rename with no symbol', 'lsp_rename', { path: 'main.go' }, '(rename in main.go)'],
        ['lsp_rename with nothing', 'lsp_rename', {}, '(rename in file)'],
    ])('writes %s', (_name, tool, args, expected) => {
        expect(formatToolArgs(tool, args)).toBe(expected);
    });
});

describe('formatToolArgs - removed lsp_code_actions tool', () => {
    it('falls back to the generic JSON summary', () => {
        expect(formatToolArgs('lsp_code_actions', { path: 'main.go', symbol: 'foo' })).toBe(
            JSON.stringify({ path: 'main.go', symbol: 'foo' }, null, 2)
        );
    });
});
