/**
 * The two directions of the address bar ↔ router state mapping: `read()`/`refresh()` adopt what the
 * bar says, `write()` decides what it says. Run against jsdom's own history, because the contract
 * these tests guard is the browser's — canonical param order, what an omitted param means, and which
 * navigation creates a Back entry.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { urlRouter } from '$lib/services/url-router.svelte';

/** Put the address bar somewhere without adding a history entry. */
function at(search: string): void {
    history.replaceState(null, '', '/' + search);
}

beforeEach(() => {
    at('');
    urlRouter.read();
    urlRouter.applyView();
});

afterEach(() => {
    vi.useRealTimers();
});

describe('read — the address bar as state', () => {
    it('adopts every param the app owns', () => {
        at('?project=p1&task=t7&view=task-explorer&file=/src/main.go&thread=th3');
        const url = urlRouter.read();
        expect(url).toEqual({ project: 'p1', task: 't7', view: 'task-explorer', file: '/src/main.go', thread: 'th3' });
        expect(urlRouter.project).toBe('p1');
        expect(urlRouter.task).toBe('t7');
        expect(urlRouter.file).toBe('/src/main.go');
        expect(urlRouter.thread).toBe('th3');
    });

    it('says empty for a param the deep link omits', () => {
        at('?project=p1');
        expect(urlRouter.read()).toEqual({ project: 'p1', task: '', view: '', file: '', thread: '' });
    });

    it('names the terminal screen as its own, replacing whatever explorer was open', () => {
        at('?project=p1&task=t7&view=task-explorer');
        urlRouter.read();
        urlRouter.applyView();
        expect(urlRouter.taskExplorerOpen).toBe(true);
        at('?project=p1&task=t7&view=terminal');
        urlRouter.read();
        expect(urlRouter.applyView()).toBe(true);
        expect(urlRouter.terminalOnly).toBe(true);
        expect(urlRouter.currentView()).toBe('terminal');
    });

    it('honours a file param only inside the roots of an explorer that can open it', () => {
        at('?project=p1&task=t7&view=task-explorer&file=/work/p1/t7/a.go');
        const url = urlRouter.read();
        urlRouter.acceptFile(url.file, ['/work/p1/t7']);
        expect(urlRouter.file).toBe('/work/p1/t7/a.go');
        // The same link back-navigated to a project that has no such worktree.
        urlRouter.acceptFile('/elsewhere/a.go', ['/work/p1/t7']);
        expect(urlRouter.file).toBe('');
    });
});

describe('write — state as the address bar', () => {
    it('writes the params in the order the link rows build their hrefs in', () => {
        urlRouter.write({ project: 'p1', task: 't7', view: 'diff' });
        expect(location.search).toBe('?project=p1&task=t7&view=diff');
    });

    it('keeps a file path readable, because a directory is named by its trailing slash', () => {
        urlRouter.write({ project: 'p1', view: 'explorer', file: '/srv/proj/src/' });
        expect(location.search).toBe('?project=p1&view=explorer&file=/srv/proj/src/');
    });

    it('clears project and task when a navigation omits them', () => {
        at('?project=p1&task=t7');
        urlRouter.read();
        urlRouter.write({ project: 'p2' });
        expect(location.search).toBe('?project=p2');
        expect(urlRouter.task).toBe('');
    });

    it('preserves view and file when a navigation omits them, and clears them when it says null', () => {
        at('?project=p1&task=t7&view=task-review&file=/srv/proj/a.go');
        urlRouter.read();
        urlRouter.write({ project: 'p1', task: 't8' });
        expect(location.search).toBe('?project=p1&task=t8&view=task-review&file=/srv/proj/a.go');
        urlRouter.write({ project: 'p1', task: 't8', view: null, file: null });
        expect(location.search).toBe('?project=p1&task=t8');
    });

    it('clears the task and its thread when a navigation omits them, and keeps the view it was not asked about', () => {
        at('?project=p1&task=t7&thread=th3');
        urlRouter.read();
        urlRouter.write({ project: 'p1', task: 't7', view: 'task-explorer' });
        expect(location.search).toBe('?project=p1&task=t7&view=task-explorer&thread=th3');
        urlRouter.write({ project: 'p1' }, true);
        expect(location.search).toBe('?project=p1&view=task-explorer');
    });

    it('keeps a thread another component pushed since this module last looked', () => {
        at('?project=p1&task=t7');
        urlRouter.read();
        // What Chat/TaskChat.svelte does when the user switches thread.
        history.pushState({ task: 't7', thread: 'th9' }, '', '/?project=p1&task=t7&thread=th9');
        urlRouter.write({ project: 'p1', task: 't7', view: 'task-explorer' });
        expect(location.search).toBe('?project=p1&task=t7&view=task-explorer&thread=th9');
    });

    it('writes no history entry for a navigation that says what the bar already says', () => {
        at('?project=p1&task=t7');
        urlRouter.read();
        const before = history.length;
        urlRouter.write({ project: 'p1', task: 't7' }, false, true);
        expect(history.length).toBe(before);
    });

    it('gives a pushed navigation a Back entry and a replaced one none', () => {
        at('?project=p1');
        urlRouter.read();
        const before = history.length;
        urlRouter.write({ project: 'p1', task: 't7' }, false, true);
        expect(history.length).toBe(before + 1);
        urlRouter.write({ project: 'p1', task: 't8' }, false, false);
        expect(history.length).toBe(before + 1);
        expect(location.search).toBe('?project=p1&task=t8');
    });
});

describe('the screen a view names', () => {
    it('opens the project explorer for view=explorer and the Review panel for view=review', () => {
        for (const [view, changes] of [['explorer', false], ['review', true]] as const) {
            at(`?project=p1&view=${view}`);
            urlRouter.read();
            urlRouter.applyView();
            expect(urlRouter.explorerOpen).toBe(true);
            expect(urlRouter.taskExplorerOpen).toBe(false);
            expect(urlRouter.changesOpen).toBe(changes);
        }
    });

    it('opens the task explorer for view=task-explorer and the Review panel for view=task-review', () => {
        for (const [view, changes] of [['task-explorer', false], ['task-review', true]] as const) {
            at(`?project=p1&task=t7&view=${view}`);
            urlRouter.read();
            urlRouter.applyView();
            expect(urlRouter.taskExplorerOpen).toBe(true);
            expect(urlRouter.explorerOpen).toBe(false);
            expect(urlRouter.changesOpen).toBe(changes);
        }
    });

    it('opens no screen for a view nobody knows, so the task detail can open instead', () => {
        at('?project=p1&task=t7&view=diff');
        urlRouter.read();
        expect(urlRouter.applyView()).toBe(false);
        expect(urlRouter.currentView()).toBe('');
    });

    it('remounts the explorer for another project without changing its view', () => {
        at('?project=p1&view=explorer');
        urlRouter.read();
        urlRouter.applyView();
        // A project switch reads the view first, then unmounts, then re-arms on the next microtask.
        expect(urlRouter.currentView()).toBe('explorer');
        expect(urlRouter.remountExplorers()).toBe(true);
        expect(urlRouter.explorerOpen).toBe(false);
        return Promise.resolve().then(() => {
            expect(urlRouter.explorerOpen).toBe(true);
            expect(urlRouter.taskExplorerOpen).toBe(false);
        });
    });

    it('reports that a definition jump is the one opening the task explorer, not the explorer itself', () => {
        expect(urlRouter.openTaskExplorer('p1', 't7')).toBe(true);
        expect(location.search).toBe('?project=p1&task=t7&view=task-explorer');
        expect(urlRouter.openTaskExplorer('p1', 't7')).toBe(false);
    });
});

describe('an address bar moved by someone else', () => {
    it('reports an external navigation once, then only after the throttle window', () => {
        vi.useFakeTimers();
        vi.setSystemTime(1_000_000);
        urlRouter.markSeen();
        // What Board.svelte does on a card click: push the task, then select it.
        history.pushState(null, '', '/?project=p1&task=t7');
        vi.advanceTimersByTime(251);
        expect(urlRouter.refresh()).toBe(true);
        expect(urlRouter.task).toBe('t7');
        history.pushState(null, '', '/?project=p1&task=t8');
        expect(urlRouter.refresh()).toBe(false);
        vi.advanceTimersByTime(251);
        expect(urlRouter.refresh()).toBe(true);
        expect(urlRouter.task).toBe('t8');
    });

    it('takes what it rendered as its own, so a change made before it is not reported', () => {
        urlRouter.write({ project: 'p1', task: 't7' }, false, true);
        urlRouter.markSeen();
        expect(urlRouter.refresh()).toBe(false);
    });
});
