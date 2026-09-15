import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
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
import ExplorerHeader from '$lib/components/ExplorerHeader.svelte';
import { projectTerminalHref, taskTerminalHref } from '$lib/utils/app-link';
import type { Project, Task } from '$lib/types';

function renderTopBar(onOpenTerminal = vi.fn()) {
    const currentProject = { id: 'p1', name: 'MIKE', path: '/home/tester/MIKE' } as Project;
    const { container } = render(TopBar, {
        props: {
            currentProject,
            currentBranch: 'main',
            onOpenMenu: vi.fn(),
            onOpenSandboxConfig: vi.fn(),
            onOpenExplorerView: vi.fn(),
            onOpenTerminal,
            onShowStats: vi.fn(),
            onShowConfig: vi.fn(),
        },
    });
    return { onOpenTerminal, button: container.querySelector('.terminal-btn') as HTMLElement };
}

function renderTaskHeader(onOpenTerminal = vi.fn(), floating = false) {
    const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;
    const { container } = render(TaskHeader, {
        props: {
            workspaceDir: '/home/tester/MIKE/.worktrees/task-1',
            taskId: 'task-1',
            sandboxEnabled: true,
            badge: { text: 'In Progress', color: '#888888' },
            onClose: vi.fn(),
            onOpenTerminal,
            onOpenExplorer: vi.fn(),
            onDelete: vi.fn(),
            floating,
            task,
        },
    });
    return { onOpenTerminal, button: container.querySelector('.terminal-btn') as HTMLElement };
}

/** Both explorer screens share this header; only the link they hand it differs. */
function renderExplorerHeader(terminalHref: string, onOpenTerminal = vi.fn()) {
    const { container } = render(ExplorerHeader, {
        props: {
            pathLabel: 'MIKE',
            closeAria: 'Close explorer',
            branch: 'main',
            changesOpen: false,
            onChangesOpen: vi.fn(),
            onCloseRequest: vi.fn(),
            onOpenTerminal,
            terminalHref,
        },
    });
    return { onOpenTerminal, button: container.querySelector('.terminal-btn') as HTMLElement };
}

function requireButton(el: HTMLElement | null): HTMLElement {
    if (!el) throw new Error('expected the terminal button to render');
    return el;
}

function clickWith(el: Element, init: Partial<MouseEventInit> = {}): boolean {
    const event = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init });
    el.dispatchEvent(event);
    return event.defaultPrevented;
}

function pressSpace(el: Element): boolean {
    const event = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true });
    el.dispatchEvent(event);
    return event.defaultPrevented;
}

/** The shape every terminal link shares: an anchor that keeps its button role and behaviour. */
function itBehavesLikeALink(
    href: string,
    open: () => { button: HTMLElement | null; onOpenTerminal: ReturnType<typeof vi.fn> },
) {
    it('is a real anchor pointing at the terminal-only screen, announced as it was before', () => {
        const button = requireButton(open().button);
        expect(button.tagName).toBe('A');
        expect(button.getAttribute('href')).toBe(href);
        expect(button.getAttribute('role')).toBe('button');
        expect(button.getAttribute('tabindex')).toBe('0');
        expect(button.getAttribute('draggable')).toBe('false');
    });

    it('opens the panel in-app on an unmodified click (no document navigation)', () => {
        const { button, onOpenTerminal } = open();
        expect(clickWith(requireButton(button))).toBe(true);
        expect(onOpenTerminal).toHaveBeenCalledTimes(1);
    });

    it('leaves a modifier click to the browser so it opens a new tab', () => {
        const { button, onOpenTerminal } = open();
        const el = requireButton(button);
        expect(clickWith(el, { ctrlKey: true })).toBe(false);
        expect(clickWith(el, { metaKey: true })).toBe(false);
        expect(clickWith(el, { shiftKey: true })).toBe(false);
        expect(clickWith(el, { altKey: true })).toBe(false);
        expect(onOpenTerminal).not.toHaveBeenCalled();
    });

    it('still activates from the keyboard (role="button" contract: Space)', () => {
        const { button, onOpenTerminal } = open();
        expect(pressSpace(requireButton(button))).toBe(true);
        expect(onOpenTerminal).toHaveBeenCalledTimes(1);
    });
}

afterEach(() => cleanup());

describe('Top-bar Terminal button is a link', () => {
    itBehavesLikeALink(projectTerminalHref('p1'), () => renderTopBar());
});

describe('Task-header Terminal button is a link', () => {
    itBehavesLikeALink(taskTerminalHref('p1', 'task-1'), () => renderTaskHeader());

    it('offers no terminal link in the floating task view', () => {
        expect(renderTaskHeader(vi.fn(), true).button).toBeNull();
    });
});

describe('Explorer-header Terminal button is a link', () => {
    it('carries the task terminal link the task explorer hands it', () => {
        const { button } = renderExplorerHeader(taskTerminalHref('p1', 'task-9'));
        expect(requireButton(button).getAttribute('href')).toBe('/?project=p1&task=task-9&view=terminal');
    });

    itBehavesLikeALink(projectTerminalHref('p1'), () => renderExplorerHeader(projectTerminalHref('p1')));
});
