import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));
vi.mock('$lib/stores', () => ({
    appStore: { config: null, projects: [{ id: 'p1', sandbox_memory_limit: 1024 }] },
    uiStore: { showCardDetail: false },
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

import TopBar from '$lib/components/App/TopBar.svelte';
import TaskHeader from '$lib/components/CardDetail/TaskHeader.svelte';
import type { Project, Task } from '$lib/types';

function renderTopBar(onOpenExplorerView = vi.fn()) {
    const currentProject = { id: 'p1', name: 'MIKE', path: '/home/tester/MIKE' } as Project;
    const { container } = render(TopBar, {
        props: {
            currentProject,
            currentBranch: 'main',
            onOpenMenu: vi.fn(),
            onOpenSandboxConfig: vi.fn(),
            onOpenExplorerView,
            onOpenTerminal: vi.fn(),
            onShowStats: vi.fn(),
            onShowConfig: vi.fn(),
        },
    });
    return { onOpenExplorerView, button: container.querySelector('.explorer-btn') as HTMLElement };
}

function renderTaskHeader(onOpenExplorer = vi.fn()) {
    const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;
    const { container } = render(TaskHeader, {
        props: {
            workspaceDir: '/home/tester/MIKE/.worktrees/task-1',
            taskId: 'task-1',
            sandboxEnabled: true,
            badge: { text: 'In Progress', color: '#888888' },
            onClose: vi.fn(),
            onOpenExplorer,
            onOpenTerminal: vi.fn(),
            onDelete: vi.fn(),
            task,
        },
    });
    const button = container.querySelector('a[aria-label="Open explorer"]') as HTMLElement;
    return { onOpenExplorer, button };
}

function requireButton(el: HTMLElement | null): HTMLElement {
    if (!el) throw new Error('expected the explorer button to render');
    return el;
}

function clickWith(el: Element, init: Partial<MouseEventInit> = {}): boolean {
    const event = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init });
    el.dispatchEvent(event);
    return event.defaultPrevented;
}

afterEach(() => cleanup());

describe('Top-bar Explorer button is a link', () => {
    it('is a real anchor pointing at the project explorer, announced as it was before', () => {
        const button = requireButton(renderTopBar().button);
        expect(button.tagName).toBe('A');
        expect(button.getAttribute('href')).toBe('/?project=p1&view=explorer');
        expect(button.getAttribute('role')).toBe('button');
        expect(button.getAttribute('tabindex')).toBe('0');
        expect(button.getAttribute('draggable')).toBe('false');
    });

    it('opens the explorer in-app on an unmodified click (no document navigation)', () => {
        const { button, onOpenExplorerView } = renderTopBar();
        expect(clickWith(requireButton(button))).toBe(true);
        expect(onOpenExplorerView).toHaveBeenCalledTimes(1);
    });

    it('leaves a modifier click to the browser so it opens a new tab', () => {
        const { button, onOpenExplorerView } = renderTopBar();
        const el = requireButton(button);
        expect(clickWith(el, { ctrlKey: true })).toBe(false);
        expect(clickWith(el, { metaKey: true })).toBe(false);
        expect(clickWith(el, { shiftKey: true })).toBe(false);
        expect(onOpenExplorerView).not.toHaveBeenCalled();
    });

    it('still activates from the keyboard (role="button" contract: Space)', () => {
        const { button, onOpenExplorerView } = renderTopBar();
        const event = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true });
        requireButton(button).dispatchEvent(event);
        expect(event.defaultPrevented).toBe(true);
        expect(onOpenExplorerView).toHaveBeenCalledTimes(1);
    });
});

describe('Task-header Explorer button is a link', () => {
    it('is a real anchor pointing at the task explorer', () => {
        const button = requireButton(renderTaskHeader().button);
        expect(button.tagName).toBe('A');
        expect(button.getAttribute('href')).toBe('/?project=p1&task=task-1&view=task-explorer');
        expect(button.getAttribute('role')).toBe('button');
        expect(button.getAttribute('tabindex')).toBe('0');
        expect(button.getAttribute('draggable')).toBe('false');
    });

    it('opens the explorer in-app on an unmodified click (no document navigation)', () => {
        const { button, onOpenExplorer } = renderTaskHeader();
        expect(clickWith(requireButton(button))).toBe(true);
        expect(onOpenExplorer).toHaveBeenCalledTimes(1);
    });

    it('leaves a modifier click to the browser so it opens a new tab', () => {
        const { button, onOpenExplorer } = renderTaskHeader();
        const el = requireButton(button);
        expect(clickWith(el, { ctrlKey: true })).toBe(false);
        expect(clickWith(el, { metaKey: true })).toBe(false);
        expect(clickWith(el, { shiftKey: true })).toBe(false);
        expect(onOpenExplorer).not.toHaveBeenCalled();
    });

    it('still activates from the keyboard (role="button" contract: Space)', () => {
        const { button, onOpenExplorer } = renderTaskHeader();
        const event = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true });
        requireButton(button).dispatchEvent(event);
        expect(event.defaultPrevented).toBe(true);
        expect(onOpenExplorer).toHaveBeenCalledTimes(1);
    });

    it('offers no explorer link in the floating task view', () => {
        const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;
        const { container } = render(TaskHeader, {
            props: {
                workspaceDir: '/home/tester/MIKE/.worktrees/task-1',
                taskId: 'task-1',
                sandboxEnabled: true,
                badge: { text: 'In Progress', color: '#888888' },
                onClose: vi.fn(),
                onDelete: vi.fn(),
                floating: true,
                task,
            },
        });
        expect(container.querySelector('a[aria-label="Open explorer"]')).toBeNull();
    });
});
