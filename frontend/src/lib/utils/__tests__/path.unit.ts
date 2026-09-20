import { describe, it, expect } from 'vitest';
import { isUnderPath, toExplorerPath } from '$lib/utils/path';

/**
 * A tool call names the file it wrote relative to the task root, while the explorer's
 * editor opens files by the absolute path of its own root — so a jump from a tool card
 * is only correct once the two path spaces agree.
 */
describe('toExplorerPath', () => {
    const worktree = '/home/mike/worktrees/my-proj/frost-amber-quartz';
    const mount = '/home/sandbox/my-proj';

    it('roots a relative path at the task worktree', () => {
        expect(toExplorerPath('src/main.go', worktree)).toBe(`${worktree}/src/main.go`);
    });

    it('roots a relative path at the sandbox mount for a sandboxed task', () => {
        expect(toExplorerPath('hello.bas', mount)).toBe(`${mount}/hello.bas`);
    });

    it('leaves an absolute path untouched — it is already in the explorer\'s space', () => {
        expect(toExplorerPath(`${mount}/src/main.go`, mount)).toBe(`${mount}/src/main.go`);
        expect(toExplorerPath(worktree + '/src/main.go', worktree)).toBe(`${worktree}/src/main.go`);
    });

    it('drops a ./ prefix rather than nesting it under the root', () => {
        expect(toExplorerPath('./src/main.go', worktree)).toBe(`${worktree}/src/main.go`);
    });

    it('does not double the separator when the root ends with one', () => {
        expect(toExplorerPath('src/main.go', worktree + '/')).toBe(`${worktree}/src/main.go`);
    });

    it('answers the path unchanged when no root is known', () => {
        expect(toExplorerPath('src/main.go', '')).toBe('src/main.go');
    });

    it('answers an empty path unchanged, so nothing opens a directory', () => {
        expect(toExplorerPath('', worktree)).toBe('');
    });
});

// A deleted folder takes every file under it; a sibling whose name only starts with it does not.
describe('isUnderPath', () => {
    const root = '/srv/proj/src';

    it('names the path itself', () => {
        expect(isUnderPath(root, root)).toBe(true);
    });

    it('names anything inside it', () => {
        expect(isUnderPath(`${root}/main.go`, root)).toBe(true);
        expect(isUnderPath(`${root}/deep/deeper/x.go`, root)).toBe(true);
    });

    it('refuses the sibling whose name merely starts with it', () => {
        expect(isUnderPath('/srv/proj/src-backup/keep.go', root)).toBe(false);
        expect(isUnderPath('/srv/proj/src2/main.go', root)).toBe(false);
    });

    it('refuses an empty side rather than treating it as a prefix of everything', () => {
        expect(isUnderPath('', root)).toBe(false);
        expect(isUnderPath(root, '')).toBe(false);
    });
});
