import { describe, it, expect } from 'vitest';

import { isArchiveViewPath } from '$lib/utils/archive-file';

/**
 * The explorer picks the viewer from the name and the server decides what the bytes are. A `.tar.gz` that
 * opened the editor would paint replacement characters, so these two have to agree on the names.
 */

describe('isArchiveViewPath', () => {
    it('opens the listing for what the server can read', () => {
        for (const path of ['a.tar', 'a.tar.gz', 'A.TGZ', '/srv/x/app.tar.bz2', 'a.tbz2', 'old.tbz', 'a.zip']) {
            expect(isArchiveViewPath(path)).toBe(true);
        }
    });

    it('opens the listing for the formats that will be refused too', () => {
        for (const path of ['a.tar.xz', 'a.txz', 'a.tar.zst', 'a.tzst', 'a.lzma', 'a.7z', 'a.rar']) {
            expect(isArchiveViewPath(path)).toBe(true);
        }
    });

    it('reads the basename, not a directory named like an archive', () => {
        expect(isArchiveViewPath('/builds/tar.gz/notes.txt')).toBe(false);
        expect(isArchiveViewPath('/builds/tar.gz/app.zip')).toBe(true);
    });

    it('leaves a name that is only its suffix to the editor', () => {
        for (const path of ['.tar.gz', '.zip', 'Makefile', 'main.go', 'shot.png', 'archive.gz', 'notes.md']) {
            expect(isArchiveViewPath(path)).toBe(false);
        }
    });
});
