// @vitest-environment jsdom
// Unit tests for turning a drop event into files + the paths they were dropped with.
import { describe, it, expect } from 'vitest';
import {
    collectDroppedFiles,
    collectPickedFiles,
    dragCarriesFiles,
    DEFAULT_FILE_SIZE_LIMIT_BYTES,
    MAX_DROP_FILES,
} from '$lib/utils/file-drop';

// The entry API is browser-internal; these build just enough of it to drive the
// walk, including the batched readEntries a real Chromium hands out.
type EntryOk = (entries: unknown[]) => void;

function fileEntry(name: string, content = 'x'): unknown {
    return {
        isFile: true,
        isDirectory: false,
        name,
        file: (ok: (f: File) => void) => ok(new File([content], name)),
    };
}

function unreadableFileEntry(name: string): unknown {
    return {
        isFile: true,
        isDirectory: false,
        name,
        file: (_ok: unknown, err: (e: unknown) => void) => err(new Error('EACCES')),
    };
}

/**
 * A directory whose children arrive in `batchSize` chunks — Chromium's real cap is
 * ~100 — counting how many readers were created so a test can prove the walk drains
 * one reader instead of restarting a new one per batch.
 */
function dirEntry(name: string, children: unknown[], batchSize = 100, stats = { readers: 0 }): unknown {
    return {
        isFile: false,
        isDirectory: true,
        name,
        stats,
        createReader(this: { stats: { readers: number } }) {
            stats.readers++;
            let cursor = 0;
            return {
                readEntries: (ok: EntryOk) => {
                    const batch = children.slice(cursor, cursor + batchSize);
                    cursor += batch.length;
                    ok(batch);
                },
            };
        },
    };
}

function dropOf(entries: unknown[], files: File[] = []): DataTransfer {
    const items = entries.map((entry) => ({ kind: 'file', webkitGetAsEntry: () => entry }));
    return { items, files } as unknown as DataTransfer;
}

const relPaths = (selection: Awaited<ReturnType<typeof collectDroppedFiles>>) =>
    selection.files.map((f) => f.relPath);

describe('dragCarriesFiles', () => {
    it('is true only for a drag that carries files', () => {
        expect(dragCarriesFiles({ types: ['Files'] } as unknown as DataTransfer)).toBe(true);
        // In-app drags (board cards, selected text) must keep working untouched.
        expect(dragCarriesFiles({ types: ['text/plain'] } as unknown as DataTransfer)).toBe(false);
        expect(dragCarriesFiles({ types: ['text/html', 'Files'] } as unknown as DataTransfer)).toBe(true);
        expect(dragCarriesFiles(null)).toBe(false);
        expect(dragCarriesFiles(undefined)).toBe(false);
    });
});

describe('collectDroppedFiles', () => {
    it('names a single dropped file by itself', async () => {
        const selection = await collectDroppedFiles(dropOf([fileEntry('notes.txt')]));
        expect(relPaths(selection)).toEqual(['notes.txt']);
        expect(selection.tooBig).toHaveLength(0);
        expect(selection.unreadable).toHaveLength(0);
    });

    it('keeps the tree of a dropped folder', async () => {
        const drop = dropOf([
            dirEntry('assets', [
                fileEntry('README.md'),
                dirEntry('img', [fileEntry('logo.png'), fileEntry('icon.svg')]),
            ]),
            fileEntry('top.txt'),
        ]);
        expect(relPaths(await collectDroppedFiles(drop))).toEqual([
            'assets/README.md',
            'assets/img/logo.png',
            'assets/img/icon.svg',
            'top.txt',
        ]);
    });

    it('drains a directory whose entries arrive in batches, on ONE reader', async () => {
        const children = Array.from({ length: 5 }, (_, i) => fileEntry(`f${i}.txt`));
        const stats = { readers: 0 };
        const selection = await collectDroppedFiles(dropOf([dirEntry('many', children, 2, stats)]));
        // Two-at-a-time over five children is three batches plus the empty one.
        expect(relPaths(selection)).toEqual(['many/f0.txt', 'many/f1.txt', 'many/f2.txt', 'many/f3.txt', 'many/f4.txt']);
        // A fresh reader restarts at the first entry: re-creating it per batch would
        // re-read f0 and f1 forever, so the walk must ask for exactly one.
        expect(stats.readers).toBe(1);
    });

    it('falls back to the flat file list when the entry API is missing', async () => {
        const files = [new File(['a'], 'a.txt'), new File(['b'], 'b.txt')];
        const drop = {
            items: [{ kind: 'file' }, { kind: 'string' }],
            files,
        } as unknown as DataTransfer;
        const selection = await collectDroppedFiles(drop);
        expect(relPaths(selection)).toEqual(['a.txt', 'b.txt']);
    });

    it('reports an unreadable entry instead of losing the drop', async () => {
        const drop = dropOf([dirEntry('mixed', [unreadableFileEntry('locked.txt'), fileEntry('ok.txt')])]);
        const selection = await collectDroppedFiles(drop);
        expect(relPaths(selection)).toEqual(['mixed/ok.txt']);
        expect(selection.unreadable).toEqual(['mixed/locked.txt']);
    });

    it('splits files over the byte cap out, so they are never sent', async () => {
        const big = new File([], 'big.bin');
        Object.defineProperty(big, 'size', { value: DEFAULT_FILE_SIZE_LIMIT_BYTES + 1 });
        const dir = dirEntry('bundle', [fileEntry('small.txt')]);
        const items = [
            { kind: 'file', webkitGetAsEntry: () => dir },
            { kind: 'file', webkitGetAsEntry: () => ({ isFile: true, isDirectory: false, name: 'big.bin', file: (ok: (f: File) => void) => ok(big) }) },
        ];
        const selection = await collectDroppedFiles({ items, files: [] } as unknown as DataTransfer);
        expect(relPaths(selection)).toEqual(['bundle/small.txt']);
        expect(selection.tooBig.map((f) => f.relPath)).toEqual(['big.bin']);
    });

    it('sorts by the cap the caller names, so a raised server limit reaches the file it would take', async () => {
        const raised = DEFAULT_FILE_SIZE_LIMIT_BYTES + (8 << 20);
        const entryNamed = (name: string, size: number) => ({
            isFile: true,
            isDirectory: false,
            name,
            file: (ok: (f: File) => void) => {
                const file = new File([], name);
                Object.defineProperty(file, 'size', { value: size });
                ok(file);
            },
        });
        const items = [
            { kind: 'file', webkitGetAsEntry: () => entryNamed('over-built-in.bin', DEFAULT_FILE_SIZE_LIMIT_BYTES + 1) },
            { kind: 'file', webkitGetAsEntry: () => entryNamed('over-named.bin', raised + 1) },
        ];
        const selection = await collectDroppedFiles({ items, files: [] } as unknown as DataTransfer, raised);
        expect(relPaths(selection)).toEqual(['over-built-in.bin']);
        expect(selection.tooBig.map((f) => f.relPath)).toEqual(['over-named.bin']);
    });

    it('stops at the ceiling and says it was truncated', async () => {
        const children = Array.from({ length: MAX_DROP_FILES + 5 }, (_, i) => fileEntry(`f${i}.txt`));
        const selection = await collectDroppedFiles(dropOf([dirEntry('huge', children, 50)]));
        expect(selection.files).toHaveLength(MAX_DROP_FILES);
        expect(selection.truncated).toBe(true);
    });

    it('reports an empty drop as nothing at all', async () => {
        const selection = await collectDroppedFiles({ items: [], files: [] } as unknown as DataTransfer);
        expect(selection.files).toHaveLength(0);
        expect(selection.tooBig).toHaveLength(0);
        expect(selection.truncated).toBe(false);
    });
});

describe('collectPickedFiles', () => {
    const picked = (name: string, size = 1) => {
        const file = new File([], name);
        Object.defineProperty(file, 'size', { value: size });
        return file;
    };

    it('names each picked file by itself, because a picker reports no path', () => {
        const selection = collectPickedFiles([picked('a.png'), picked('b.png')]);
        expect(relPaths(selection)).toEqual(['a.png', 'b.png']);
        expect(selection.tooBig).toHaveLength(0);
        expect(selection.truncated).toBe(false);
    });

    it('splits a picked file over the byte cap out, as a drop does', () => {
        const selection = collectPickedFiles([picked('small.txt'), picked('big.bin', DEFAULT_FILE_SIZE_LIMIT_BYTES + 1)]);
        expect(relPaths(selection)).toEqual(['small.txt']);
        expect(selection.tooBig.map((f) => f.relPath)).toEqual(['big.bin']);
    });

    it('splits a picked file by the cap the caller names, never by its own', () => {
        const raised = 2 * DEFAULT_FILE_SIZE_LIMIT_BYTES;
        const selection = collectPickedFiles([
            picked('over-built-in.bin', DEFAULT_FILE_SIZE_LIMIT_BYTES + 1),
            picked('over-named.bin', raised + 1),
        ], raised);
        expect(relPaths(selection)).toEqual(['over-built-in.bin']);
        expect(selection.tooBig.map((f) => f.relPath)).toEqual(['over-named.bin']);
    });

    it('counts oversize picks toward the ceiling, so the cap bounds the whole run', () => {
        const files = Array.from({ length: MAX_DROP_FILES + 5 }, (_, i) =>
            picked(`f${i}.bin`, i % 2 ? DEFAULT_FILE_SIZE_LIMIT_BYTES + 1 : 1),
        );
        const selection = collectPickedFiles(files);
        expect(selection.files.length + selection.tooBig.length).toBe(MAX_DROP_FILES);
        expect(selection.truncated).toBe(true);
    });

    it('reports an empty pick as nothing at all', () => {
        expect(collectPickedFiles([]).files).toHaveLength(0);
    });
});
