import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

import Sidebar from '$lib/components/App/Sidebar.svelte';
import Column from '$lib/components/Column.svelte';
import Card from '$lib/components/Card.svelte';
import ToggleSwitch from '$lib/components/ToggleSwitch.svelte';
import { appStore, uiStore } from '$lib/stores';
import type { Project, Task } from '$lib/types';

// Card pulls in transitions, raw SVGs and the app-level contexts App normally provides.
vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

vi.mock('$lib/context', () => {
    function writable(initial: unknown) {
        return {
            subscribe: (fn: (v: unknown) => void) => { fn(initial); return () => { }; },
            set: () => { },
            update: () => { },
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

// useIsMobile asks for matchMedia; desktop means "never matches".
beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => { },
        removeEventListener: () => { },
        addListener: () => { },
        removeListener: () => { },
        dispatchEvent: () => false,
    }));
});

afterEach(cleanup);

/**
 * Accessibility of the board's building blocks, asserted the only way that means
 * anything: render the component and read the attributes off the nodes the browser
 * would actually hand to a screen reader.
 */
describe('Accessibility (Section 508 / WCAG 2.1 AA)', () => {
    function createMockProject(): Project {
        return {
            id: 'test-project',
            name: 'test-project',
            path: '/home/test-project',
            sandbox_enabled: false,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
        };
    }

    function renderSidebar(overrides: Partial<Record<string, unknown>> = {}) {
        const project = createMockProject();
        return render(Sidebar, {
            projects: [project],
            currentProject: project,
            projectCounts: { 'test-project': { backlog: 0, in_progress: 0, review: 0 } },
            collapsed: false,
            isMobile: false,
            mobileMenuOpen: false,
            getProjectAbbr: (name: string) => name.slice(0, 2).toUpperCase(),
            onSelectProject: vi.fn(),
            onRemoveProject: vi.fn(),
            onToggleSidebar: vi.fn(),
            onShowStats: vi.fn(),
            onShowConfig: vi.fn(),
            onToggleFullScreen: vi.fn(),
            onAddProject: vi.fn(),
            ...overrides,
        });
    }

    describe('Sidebar', () => {
        it('labels its project navigation', () => {
            const { container } = renderSidebar();
            expect(container.querySelector('nav[aria-label="Projects"]')).toBeTruthy();
        });

        it('exposes the collapse state with aria-expanded on the control that owns it', () => {
            const { container } = renderSidebar();
            const toggleBtn = container.querySelector('[aria-controls="sidebar"]');
            expect(toggleBtn).toBeTruthy();
            expect(['true', 'false']).toContain(toggleBtn?.getAttribute('aria-expanded'));
        });

        it('hides decorative SVGs from the accessibility tree', () => {
            const { container } = renderSidebar();
            expect(container.querySelectorAll('button > svg[aria-hidden="true"]').length)
                .toBeGreaterThan(0);
        });
    });

    describe('Card', () => {
        function makeTask(overrides: Partial<Task> = {}): Task {
            return {
                id: 'a11y-1',
                project_id: 'test-project',
                title: 'Accessible card',
                prompt: 'do something',
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
                parent_task_id: '',
                created_at: new Date().toISOString(),
                updated_at: new Date().toISOString(),
                moved_at: new Date().toISOString(),
                ...overrides,
            } as Task;
        }

        function renderCard(task = makeTask(), childrenMap = new Map<string, Task[]>(), columnId = 'backlog') {
            const onSelect = vi.fn();
            const onToggleExpanded = vi.fn();
            const { container } = render(Card, {
                task,
                columnId,
                projectId: 'test-project',
                childrenMap,
                expandedTasks: new Set<string>(),
                onToggleExpanded,
                onCreateChildTask: vi.fn(),
                columnColor: '#888888',
                inline: false,
                onSelect,
                parentSelected: false,
                showSubtaskBorder: false,
                onRefreshBoard: vi.fn(),
                multiSelect: null as never,
            });
            return { container, onSelect, onToggleExpanded };
        }

        it('is a focusable button announcing the task and its status', () => {
            const { container } = renderCard();
            const card = container.querySelector('.card')!;
            expect(card.getAttribute('role')).toBe('button');
            expect(card.getAttribute('tabindex')).toBe('0');
            expect(card.getAttribute('aria-label')).toContain('Accessible card');
        });

        it('activates from the keyboard', async () => {
            const { container } = renderCard();
            const card = container.querySelector('.card')!;

            await fireEvent.keyDown(card, { key: 'Enter' });

            expect(appStore.selectedTask?.id).toBe('a11y-1');
            expect(uiStore.showCardDetail).toBe(true);
            appStore.selectedTask = null;
            uiStore.showCardDetail = false;
        });

        it('names every icon-only action', () => {
            const { container } = renderCard();
            expect(container.querySelector('button[aria-label="Create subtask"]')).toBeTruthy();
            expect(container.querySelector('button[aria-label="Edit task"]')).toBeTruthy();
            expect(container.querySelector('button[aria-label^="Move to"]')).toBeTruthy();

            // The delete control only exists in Done, but the rule is the same anywhere.
            const { container: done } = renderCard(makeTask({ status: 'done' }), new Map(), 'done');
            expect(done.querySelector('button[aria-label="Delete task"]')).toBeTruthy();

            // An icon-only control that names itself only in `title` is invisible to a
            // screen reader — assert there are none left in the action row.
            for (const root of [container, done]) {
                for (const btn of root.querySelectorAll('.card-actions button')) {
                    expect(btn.getAttribute('aria-label'), btn.className).toBeTruthy();
                }
            }
        });

        it('exposes the subtask list expansion state and toggles it', async () => {
            const parent = makeTask({ id: 'a11y-parent' });
            const child = makeTask({ id: 'a11y-child', parent_task_id: 'a11y-parent' });
            const { container, onToggleExpanded } = renderCard(parent, new Map([['a11y-parent', [child]]]));

            const bar = container.querySelector<HTMLButtonElement>('.subtask-progress-bar')!;
            expect(bar).toBeTruthy();
            expect(bar.getAttribute('aria-expanded')).toBe('false');

            await fireEvent.click(bar);
            expect(onToggleExpanded).toHaveBeenCalledWith('a11y-parent');
        });
    });

    describe('ToggleSwitch', () => {
        it('is a switch that reports its own state', () => {
            const { container } = render(ToggleSwitch, { checked: true, ariaLabel: 'Sandbox' });
            const toggle = container.querySelector('[role="switch"]')!;
            expect(toggle.getAttribute('aria-checked')).toBe('true');
            expect(toggle.getAttribute('aria-label')).toBe('Sandbox');
        });

        it('flips its state and reports the change on click', async () => {
            const onChange = vi.fn();
            const { container } = render(ToggleSwitch, { checked: false, onChange });
            const toggle = container.querySelector<HTMLButtonElement>('[role="switch"]')!;

            await fireEvent.click(toggle);

            expect(onChange).toHaveBeenCalledTimes(1);
            expect(toggle.getAttribute('aria-checked')).toBe('true');
        });

        it('stays put when disabled', async () => {
            const onChange = vi.fn();
            const { container } = render(ToggleSwitch, { checked: false, disabled: true, onChange });
            await fireEvent.click(container.querySelector('[role="switch"]')!);
            expect(onChange).not.toHaveBeenCalled();
        });
    });

    describe('Column', () => {
        it('has ARIA roles for drag targets', () => {
            const { container } = render(Column, {
                columnId: 'backlog',
                title: 'Backlog',
                color: 'blue',
                tasks: [],
                projectId: 'test-project',
                childrenMap: new Map(),
                expandedTasks: new Set<string>(),
                onToggleExpanded: vi.fn(),
                onCreateChildTask: vi.fn(),
                onRefreshBoard: vi.fn(),
                onDrop: vi.fn(),
            });

            const column = container.querySelector('.column');
            expect(column).toBeTruthy();
            expect(column?.getAttribute('role')).toBe('list');
        });
    });
});
