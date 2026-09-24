import { describe, it, expect } from 'vitest';
import { fileBase, fileSuffix, isVimSwapFile } from '$lib/utils/file-suffix';

/**
 * `isVimSwapFile` mirrors the vim clause of `git.ShouldIgnoreFile` (`backend/git/worktree.go`), which
 * decides what the server never broadcasts a change for. The four extensions and the leading dot are
 * the rule; a name that matches here is a file the diff panel must not show, and one that does not is
 * a file the person wrote.
 */
describe('the vim swap and undo files the server hides', () => {
    it('catches all four extensions, on any name', () => {
        for (const suffix of ['swp', 'swo', 'ssn', 'uno']) {
            expect(isVimSwapFile(`/p/.main.go.${suffix}`)).toBe(true);
        }
    });

    it('catches one in a directory, and one whose name holds no dot but the extension', () => {
        expect(isVimSwapFile('.main.go.swp')).toBe(true);
        expect(isVimSwapFile('/deep/nested/dir/.notes.uno')).toBe(true);
    });

    it('refuses a tracked file that only shares the extension', () => {
        expect(isVimSwapFile('/p/notes.swp')).toBe(false); // not a dotfile: someone's file
        expect(isVimSwapFile('/p/main.go')).toBe(false);
        expect(isVimSwapFile('/p/backup~')).toBe(false);
    });

    it('refuses the dotfile that is itself a name, not a swap of one', () => {
        expect(isVimSwapFile('/p/.gitignore')).toBe(false);
        expect(isVimSwapFile('/p/.env.swp.bak')).toBe(false);
    });
});

describe('the suffix a file type is matched on', () => {
    it('is the trailing segment lowercased, dotfile included', () => {
        expect(fileBase('/p/App.TSX')).toBe('app.tsx');
        expect(fileSuffix('/p/App.TSX')).toBe('tsx');
    });

    it('is empty for a dotfile and for a name with no dot', () => {
        expect(fileSuffix('/p/.gitignore')).toBe('');
        expect(fileSuffix('/p/Makefile')).toBe('');
    });
});
