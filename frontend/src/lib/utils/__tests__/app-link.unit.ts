import { describe, it, expect } from 'vitest';
import {
    projectTerminalHref, taskTerminalHref, taskViewHref, asksForTerminal,
    explorerPathHref, parseExplorerFile, explorerPathRoots, fileWithinRoots,
} from '../app-link';

const path = location.pathname;

describe('app deep links', () => {
    it('names the project then the view, as updateUrl orders them', () => {
        expect(projectTerminalHref('p1')).toBe(`${path}?project=p1&view=terminal`);
    });

    it('puts the task between project and view', () => {
        expect(taskTerminalHref('p1', 't2')).toBe(`${path}?project=p1&task=t2&view=terminal`);
    });

    it('encodes a project id so it cannot add params of its own', () => {
        expect(projectTerminalHref('a&x=1')).toBe(`${path}?project=a%26x%3D1&view=terminal`);
    });

    it('names the task view the way updateUrl writes it: project, task, no view', () => {
        expect(taskViewHref('p1', 't2')).toBe(`${path}?project=p1&task=t2`);
        expect(asksForTerminal(taskViewHref('p1', 't2').slice(path.length))).toBe(false);
    });

    it('encodes a task id so it cannot add params of its own', () => {
        expect(taskViewHref('p1', 'a&x=1')).toBe(`${path}?project=p1&task=a%26x%3D1`);
    });

    it('reads the terminal screen back out of a query', () => {
        expect(asksForTerminal('?project=p1&view=terminal')).toBe(true);
        expect(asksForTerminal('?project=p1&task=t2&view=terminal')).toBe(true);
    });

    it('is not the terminal screen for any other view or no view', () => {
        expect(asksForTerminal('?project=p1')).toBe(false);
        expect(asksForTerminal('?project=p1&view=explorer')).toBe(false);
        expect(asksForTerminal('?project=p1&view=task-terminal')).toBe(false);
        expect(asksForTerminal('')).toBe(false);
    });
});

describe('the link a file row hands a new window', () => {
    it('names the project explorer and keeps the path readable', () => {
        expect(explorerPathHref('p1', '', '/home/dev/proj/src/a.ts', false))
            .toBe(`${path}?project=p1&view=explorer&file=/home/dev/proj/src/a.ts`);
    });

    it('is the task explorer when a task is named', () => {
        expect(explorerPathHref('p1', 't2', '/ws/p1/t2/a.ts', false))
            .toBe(`${path}?project=p1&task=t2&view=task-explorer&file=/ws/p1/t2/a.ts`);
    });

    it('marks a folder with the trailing slash the reader looks for', () => {
        expect(explorerPathHref('p1', '', '/home/dev/proj/src', true))
            .toBe(`${path}?project=p1&view=explorer&file=/home/dev/proj/src/`);
        expect(explorerPathHref('p1', '', '/home/dev/proj/src/', true))
            .toBe(`${path}?project=p1&view=explorer&file=/home/dev/proj/src/`);
    });

    it('encodes a name without letting it add a param of its own', () => {
        const href = explorerPathHref('p1', '', '/ws/a b&file=.ts#2', false);
        expect(href).toBe(`${path}?project=p1&view=explorer&file=/ws/a%20b%26file%3D.ts%232`);
        expect(new URLSearchParams(href.slice(path.length)).get('file')).toBe('/ws/a b&file=.ts#2');
    });

    it('reads the trailing slash back as a folder', () => {
        expect(parseExplorerFile('/home/dev/proj/src/')).toEqual({ path: '/home/dev/proj/src', isDir: true });
        expect(parseExplorerFile('/home/dev/proj/src/a.ts')).toEqual({ path: '/home/dev/proj/src/a.ts', isDir: false });
        expect(parseExplorerFile('')).toEqual({ path: '', isDir: false });
    });
});

describe('the roots a file param has to sit inside', () => {
    const project = {
        projectPath: '/home/dev/MIKE', projectName: 'MIKE', sandboxEnabled: false,
        workspaceDir: '/home/dev/ws', projectId: 'p1',
    };

    it('is the checkout, and nothing else, for a project that is not sandboxed', () => {
        expect(explorerPathRoots(project)).toEqual(['/home/dev/MIKE']);
        expect(explorerPathRoots({ ...project, taskId: 't2' }))
            .toEqual(['/home/dev/MIKE', '/home/dev/ws/p1/t2']);
    });

    it('adds each mount the same directory appears under in a container', () => {
        expect(explorerPathRoots({ ...project, sandboxEnabled: true, taskId: 't2' }))
            .toEqual(['/home/dev/MIKE', '/home/dev/ws/p1/t2', '/home/sandbox/MIKE']);
    });

    it('names the project mount after its directory when that is not its name', () => {
        expect(explorerPathRoots({ ...project, projectPath: '/home/dev/mike-checkout', sandboxEnabled: true }))
            .toEqual(['/home/dev/mike-checkout', '/home/sandbox/mike-checkout', '/home/sandbox/MIKE']);
    });

    it('accepts a path under one root and refuses one outside them all', () => {
        const roots = explorerPathRoots({ ...project, taskId: 't2' });
        expect(fileWithinRoots('/home/dev/MIKE/src/a.ts', roots)).toBe(true);
        expect(fileWithinRoots('/home/dev/ws/p1/t2/src/', roots)).toBe(true);
        expect(fileWithinRoots('', roots)).toBe(false);
        expect(fileWithinRoots('/home/dev/other/a.ts', roots)).toBe(false);
        expect(fileWithinRoots('/home/sandbox/MIKE/a.ts', explorerPathRoots({ ...project, taskId: 't2' }))).toBe(false);
    });
});
