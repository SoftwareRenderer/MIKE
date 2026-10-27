import { describe, it, expect } from 'vitest';
import {
    baseName, getWorktreeDisplayPath, isUnderPath, parentDir, sandboxMountFromPath,
    sandboxMountPath, toExplorerPath, toHomeRelative,
} from '$lib/utils/path';

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

/**
 * The two cuts every listing, picker and explorer makes on a path. `parentDir` is the folder a row
 * lives in — the `..` row, the paste destination, the folder a deep link lists — and `baseName` is the
 * row's label. Both are one cut at the last separator, so they are written once.
 */
describe('the two cuts on a path', () => {
    it('a directory above the root of the filesystem is the root', () => {
        expect(parentDir('/home')).toBe('/');
        expect(parentDir('/home/mike/proj/main.go')).toBe('/home/mike/proj');
    });

    it('a path with no separator has no folder of its own', () => {
        expect(parentDir('solo')).toBe('');
    });

    it('a row is called by its last segment, case kept', () => {
        expect(baseName('/home/sandbox/bear/main.go')).toBe('main.go');
        expect(baseName('main.go')).toBe('main.go');
        expect(baseName('/p/App.TSX')).toBe('App.TSX');
    });

    it('the folder a deep link names is the same cut as the `..` row', () => {
        // The explorers list this folder; the picker walks to it. One answer, from both.
        expect(parentDir('/p/docs/readme.md')).toBe('/p/docs');
        expect(baseName('/p/docs/readme.md')).toBe('readme.md');
    });
});

describe('the display path', () => {
    it('shortens a path under a home directory', () => {
        expect(toHomeRelative('/home/mike/repos/kanban/.worktrees/proj-1/frost-amber'))
            .toBe('~/repos/kanban/.worktrees/proj-1/frost-amber');
    });

    it('shows a path outside any home directory whole', () => {
        expect(toHomeRelative('/mike/worktrees/proj-1/frost-amber')).toBe('/mike/worktrees/proj-1/frost-amber');
        expect(toHomeRelative('/mike/projects/kanban')).toBe('/mike/projects/kanban');
    });

    it('leaves a home directory with nothing under it alone', () => {
        expect(toHomeRelative('/home/mike')).toBe('/home/mike');
    });

    it('answers an empty path with no label', () => {
        expect(toHomeRelative('')).toBe('');
    });

    it('keeps the worktree folders in front of the task ID', () => {
        expect(getWorktreeDisplayPath('/mike/worktrees', 'proj-1', 'frost-amber'))
            .toBe('/mike/worktrees/proj-1/frost-amber');
        expect(getWorktreeDisplayPath('/home/mike/kanban/.worktrees/', 'proj-1', 'frost-amber'))
            .toBe('~/kanban/.worktrees/proj-1/frost-amber');
    });

    it('answers nothing when the worktree root is unknown', () => {
        expect(getWorktreeDisplayPath('', 'proj-1', 'frost-amber')).toBe('');
    });
});

/**
 * The container-side path is a mirror of `sandbox.MountRoot` and of the mount the runtime names after
 * the project, so a path an agent reported has to resolve here exactly as the server laid it out.
 */
describe('the container mount', () => {
    it('names the mount after the tree it shows', () => {
        expect(sandboxMountPath('bear')).toBe('/home/sandbox/bear');
    });

    it('answers no mount for no name, rather than mounting the string "undefined"', () => {
        expect(sandboxMountPath('')).toBe('');
    });

    it('takes a project mount from the host checkout\'s own directory', () => {
        expect(sandboxMountFromPath('/home/mike/proj')).toBe('/home/sandbox/proj');
        expect(sandboxMountFromPath('/home/mike/proj/')).toBe('/home/sandbox/proj');
        expect(sandboxMountFromPath('')).toBe('');
    });
});
