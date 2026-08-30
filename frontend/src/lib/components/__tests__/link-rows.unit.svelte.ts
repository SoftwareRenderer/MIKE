import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mocks needed by Card (same shape as card-action-padding.unit.svelte.ts).
vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

vi.mock('$lib/context', () => {
    function writable(initial: unknown) {
        return {
            subscribe: (fn: (v: unknown) => void) => { fn(initial); return () => {}; },
            set: () => {},
            update: () => {},
        };
    }
    return {
        getStreamingContext: () => writable(new Set<string>()),
        getTaskDiffStatsContext: () => writable({}),
        getCommitQueueContext: () => writable({}),
        getExpandedTasksContext: () => writable(new Set<string>()),
        getTaskEventContext: () => writable(null),
    };
});

// Card reads the app store for the multi-select binding — keep it writable per test.
const { mockAppStore } = vi.hoisted(() => ({
    mockAppStore: {
        selectedTask: null,
        board: null,
        config: { providers: [] } as Record<string, unknown>,
        editTaskData: null,
        showEditTask: false,
        selectedProject: null,
        projects: [],
    },
}));

vi.mock('$lib/stores', () => ({
    appStore: mockAppStore,
    uiStore: { showCardDetail: false },
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

import Sidebar from '$lib/components/App/Sidebar.svelte';
import Card from '$lib/components/Card.svelte';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
import type { Project, Task } from '$lib/types';

// Polyfill matchMedia for useIsMobile (desktop: never matches)
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

afterEach(() => {
    cleanup();
    mockAppStore.config = { providers: [] };
});

function makeProject(id = 'proj-1'): Project {
    return {
        id,
        name: 'MIKE',
        path: '/home/test/MIKE',
        sandbox_enabled: true,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
    } as Project;
}

function makeTask(overrides: Partial<Task> = {}): Task {
    return {
        id: 'frost-amber-quartz',
        project_id: 'proj-1',
        title: 'Cards should behave like links',
        prompt: 'do it',
        status: 'backlog',
        execution_state: '',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: true,
        auto_merge: true,
        subtasks_auto_merge: true,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...overrides,
    } as Task;
}

function renderSidebar(props: { projects?: Project[]; onSelectProject?: (p: Project) => void } = {}) {
    const onSelectProject = props.onSelectProject ?? vi.fn();
    const projects = props.projects ?? [makeProject()];
    const { container } = render(Sidebar, {
        projects,
        currentProject: projects[0],
        projectCounts: { 'proj-1': { backlog: 0, in_progress: 0, review: 0 } },
        collapsed: false,
        isMobile: false,
        mobileMenuOpen: false,
        getProjectAbbr: (name: string) => name.slice(0, 2).toUpperCase(),
        onSelectProject,
        onRemoveProject: vi.fn(),
        onToggleSidebar: vi.fn(),
        onShowStats: vi.fn(),
        onShowConfig: vi.fn(),
        onToggleFullScreen: vi.fn(),
        onAddProject: vi.fn(),
    });
    return { onSelectProject, row: container.querySelector('.project-item') as HTMLElement };
}

function renderCard(props: { task?: Task; projectId?: string; multiSelect?: MultiSelect | null; onSelect?: (t: Task) => void } = {}) {
    const onSelect = props.onSelect ?? vi.fn();
    const { container } = render(Card, {
        task: props.task ?? makeTask(),
        columnId: 'backlog',
        projectId: props.projectId === undefined ? 'proj-1' : props.projectId,
        childrenMap: new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: false,
        onSelect,
        parentSelected: false,
        onRefreshBoard: vi.fn(),
        multiSelect: (props.multiSelect ?? null) as never,
    });
    return {
        onSelect,
        card: container.querySelector('.card') as HTMLElement,
        link: container.querySelector('a.card-open-link') as HTMLAnchorElement | null,
    };
}

/** Narrow the overlay to a present element (its absence is a failure, not a null). */
function requireLink(link: HTMLAnchorElement | null): HTMLAnchorElement {
    if (!link) throw new Error('expected the card to render its link overlay');
    return link;
}

/** Dispatch a real bubbling, cancelable click and report whether the app cancelled it. */
function clickWith(el: Element, init: Partial<MouseEventInit> = {}): boolean {
    const event = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init });
    el.dispatchEvent(event);
    return event.defaultPrevented;
}

describe('Sidebar project row is a link', () => {
    it('is a real anchor pointing at ?project=<id>, announced as it was before', () => {
        const { row } = renderSidebar();
        expect(row.tagName).toBe('A');
        expect(row.getAttribute('href')).toBe('/?project=proj-1');
        expect(row.getAttribute('role')).toBe('button');
        expect(row.getAttribute('tabindex')).toBe('0');
    });

    it('opens the project in-app on an unmodified click (no document navigation)', () => {
        const { row, onSelectProject } = renderSidebar();
        const prevented = clickWith(row);
        expect(prevented).toBe(true);
        expect(onSelectProject).toHaveBeenCalledTimes(1);
    });

    it('leaves a modifier click to the browser so it opens a new tab', () => {
        const { row, onSelectProject } = renderSidebar();
        expect(clickWith(row, { ctrlKey: true })).toBe(false);
        expect(clickWith(row, { metaKey: true })).toBe(false);
        expect(clickWith(row, { shiftKey: true })).toBe(false);
        expect(onSelectProject).not.toHaveBeenCalled();
    });

    it('still activates from the keyboard (role="button" contract: Space)', () => {
        const { row, onSelectProject } = renderSidebar();
        const event = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true });
        row.dispatchEvent(event);
        expect(event.defaultPrevented).toBe(true);
        expect(onSelectProject).toHaveBeenCalledTimes(1);
    });

    it('has no href and stays inert while the project is still cloning', () => {
        const cloning = { ...makeProject(), cloning: true, cloneProgress: 42 } as Project;
        const { row, onSelectProject } = renderSidebar({ projects: [cloning] });
        expect(row.getAttribute('href')).toBeNull();
        expect(row.getAttribute('tabindex')).toBe('0');
        // With no href there is nothing to cancel, and the row selects nothing.
        expect(clickWith(row)).toBe(false);
        expect(onSelectProject).not.toHaveBeenCalled();
    });
});

describe('Task card link overlay', () => {
    it('covers the card with an anchor to the task deep link', () => {
        const { link, card } = renderCard();
        const overlay = requireLink(link);
        expect(overlay.getAttribute('href')).toBe('/?project=proj-1&task=frost-amber-quartz');
        expect(card.contains(link)).toBe(true);
        // Decorative for assistive tech: the card root stays the accessible control.
        expect(overlay.getAttribute('aria-hidden')).toBe('true');
        expect(overlay.getAttribute('tabindex')).toBe('-1');
        // The card — not the URL — stays the drag source.
        expect(overlay.getAttribute('draggable')).toBe('false');
        expect(card.getAttribute('role')).toBe('button');
        expect(card.getAttribute('draggable')).toBe('true');
    });

    it('renders no link when the project is unknown', () => {
        const { link, card } = renderCard({ projectId: '' });
        expect(link).toBeNull();
        expect(card).toBeTruthy();
    });

    it('opens the task in-app on a plain click over the overlay', () => {
        const { link, onSelect } = renderCard();
        expect(clickWith(requireLink(link))).toBe(true);
        expect(onSelect).toHaveBeenCalledTimes(1);
    });

    it('claims the bound modifier for multi-select and cancels the navigation', () => {
        // jsdom is not a Mac, so the default binding is Ctrl+Click.
        const multiSelect = new MultiSelect();
        const { link, onSelect } = renderCard({ multiSelect });
        const prevented = clickWith(requireLink(link), { ctrlKey: true });
        expect(prevented).toBe(true);
        expect(multiSelect.isSelected('frost-amber-quartz')).toBe(true);
        expect(onSelect).not.toHaveBeenCalled();
    });

    it('honours a rebound modifier: an unbound Ctrl+Click reaches the browser', () => {
        mockAppStore.config = { providers: [], keyboard_shortcuts: { multi_select_click: 'shift+click' } };
        const multiSelect = new MultiSelect();
        const { link, onSelect } = renderCard({ multiSelect });
        // Ctrl+Click is the browser's new-tab gesture now — nothing cancelled it.
        expect(clickWith(requireLink(link), { ctrlKey: true })).toBe(false);
        expect(multiSelect.count).toBe(0);
        expect(onSelect).not.toHaveBeenCalled();
        // Shift+Click is the app's.
        expect(clickWith(requireLink(link), { shiftKey: true })).toBe(true);
        expect(multiSelect.isSelected('frost-amber-quartz')).toBe(true);
    });

    it('releases every modifier click when the binding is disabled', () => {
        mockAppStore.config = { providers: [], keyboard_shortcuts: { multi_select_click: '' } };
        const multiSelect = new MultiSelect();
        const { link, onSelect } = renderCard({ multiSelect });
        expect(clickWith(requireLink(link), { ctrlKey: true })).toBe(false);
        expect(clickWith(requireLink(link), { metaKey: true })).toBe(false);
        expect(multiSelect.count).toBe(0);
        expect(onSelect).not.toHaveBeenCalled();
    });

    it('does not double-handle the click on the card root', () => {
        const { link, onSelect } = renderCard();
        clickWith(requireLink(link));
        expect(onSelect).toHaveBeenCalledTimes(1);
    });

    it('gives a nested subtask card its own link and keeps the parent out of it', () => {
        const parent = makeTask({ id: 'parent-1' });
        const child = makeTask({ id: 'child-2', parent_task_id: 'parent-1' });
        const onSelect = vi.fn();
        const { container } = render(Card, {
            task: parent,
            columnId: 'backlog',
            projectId: 'proj-1',
            childrenMap: new Map<string, Task[]>([['parent-1', [child]]]),
            // Board owns expansion: the parent is expanded, so the child renders inside it.
            expandedTasks: new Set<string>(['parent-1']),
            onToggleExpanded: vi.fn(),
            onCreateChildTask: vi.fn(),
            columnColor: '#888888',
            inline: false,
            onSelect,
            parentSelected: false,
            onRefreshBoard: vi.fn(),
            multiSelect: null as never,
        });
        const links = [...container.querySelectorAll('a.card-open-link')];
        expect(links).toHaveLength(2);
        expect(links[0].getAttribute('href')).toContain('task=parent-1');
        expect(links[1].getAttribute('href')).toContain('task=child-2');

        clickWith(links[1]);
        expect(onSelect).toHaveBeenCalledTimes(1);
        expect((onSelect.mock.calls[0][0] as Task).id).toBe('child-2');
    });
});
