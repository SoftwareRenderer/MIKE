/**
 * A translated label is data, never markup. The overflow menu takes its labels from
 * the active translation pack, so it builds rows with DOM APIs and text nodes.
 */
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// Standalone Card needs the contexts App normally provides.
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

// The overflow button only renders for a *mobile* subtask.
beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: true,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

vi.mock('$lib/stores', () => ({
    appStore: {
        selectedTask: null,
        board: null,
        config: { providers: [], worker_provider: '', worker_model: '' },
        editTaskData: null,
        showEditTask: false,
        selectedProject: null,
    },
    uiStore: {},
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

import Card from '$lib/components/Card.svelte';
import { registerCatalog, setLocale, resetToEnglish } from '$lib/i18n/runtime.svelte';
import type { Task } from '$lib/types';

/** Markup a pack could ship as the translation of `board.card.edit`. */
const MARKUP_LABEL = '<img class="probe" src="x"><style>.probe{position:fixed}</style>';

function makeTask(overrides: Partial<Task> = {}): Task {
    return {
        id: 'ovf-1',
        project_id: 'test-project',
        title: 'Overflow menu task',
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
        parent_task_id: 'ovf-parent',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...overrides,
    } as Task;
}

function openOverflowMenu(task = makeTask(), columnId = 'backlog', extra: Record<string, unknown> = {}) {
    const { container } = render(Card, {
        task,
        columnId,
        projectId: 'test-project',
        childrenMap: new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: true,
        onSelect: vi.fn(),
        parentSelected: false,
        showSubtaskBorder: false,
        onRefreshBoard: vi.fn(),
        multiSelect: null as never,
        ...extra,
    });
    const button = container.querySelector('.action-overflow') as HTMLButtonElement;
    expect(button).toBeTruthy();
    return fireEvent.click(button);
}

describe('Card overflow menu labels', () => {
    afterEach(() => {
        cleanup();
        resetToEnglish();
        setLocale(null);
        document.body.innerHTML = '';
    });

    it('renders a pack-authored label as text, not as markup', async () => {
        registerCatalog('xx', { 'board.card.edit': MARKUP_LABEL });
        setLocale('xx');
        await openOverflowMenu();

        const items = [...document.querySelectorAll('.overflow-item')];
        expect(items.length).toBeGreaterThan(0);
        const edited = items.find((el) => el.textContent?.includes('probe'));
        expect(edited).toBeTruthy();

        // The pack's string is still readable to the user …
        expect(edited!.textContent).toBe(MARKUP_LABEL);
        // … but nothing of it became an element.
        expect(edited!.querySelector('img')).toBeNull();
        expect(edited!.querySelector('style')).toBeNull();
        expect(document.querySelector('style.probe')).toBeNull();
    });

    it('keeps the icon for every row while building it without markup', async () => {
        // Three renders cover all five icon shapes: subtask + edit (backlog),
        // pause (a running in-progress card), resume (a paused one), and delete
        // (a done-column card).
        const variants: Array<[Task, string, Record<string, unknown>?]> = [
            // `showSubtaskBorder` is what makes a subtask card offer "create subtask".
            [makeTask(), 'backlog', { showSubtaskBorder: true }],
            [makeTask({ status: 'in_progress', execution_state: 'running' }), 'in_progress'],
            [makeTask({ status: 'in_progress', execution_state: 'paused' }), 'in_progress'],
            [makeTask({ status: 'done' }), 'done'],
        ];
        const shapes = new Set<string>();

        for (const [task, column, extra] of variants) {
            await openOverflowMenu(task, column, extra);
            const items = [...document.querySelectorAll('.overflow-item')];
            expect(items.length).toBeGreaterThan(0);
            for (const item of items) {
                const svg = item.querySelector('svg');
                expect(svg).toBeTruthy();
                expect(svg!.getAttribute('viewBox')).toBe('0 0 16 16');
                expect(svg!.getAttribute('fill')).toBe('none');
                expect(svg!.style.flexShrink).toBe('0');
                // Every icon is at least one shape, built in the SVG namespace.
                const children = [...svg!.children];
                expect(children.length).toBeGreaterThan(0);
                for (const shape of children) {
                    expect(shape.namespaceURI).toBe('http://www.w3.org/2000/svg');
                    expect(
                        shape.getAttribute('stroke') ?? shape.getAttribute('fill')
                    ).toBe('currentColor');
                    expect(
                        shape.getAttribute('d') ?? shape.getAttribute('points') ?? shape.getAttribute('width')
                    ).toBeTruthy();
                    shapes.add(`${shape.tagName}|${shape.getAttribute('d') ?? shape.getAttribute('points') ?? [shape.getAttribute('x'), shape.getAttribute('y'), shape.getAttribute('width'), shape.getAttribute('height')].join(',')}`);
                }
            }
            cleanup();
            // The menu lives on document.body, so unmounting the card leaves it
            // behind — drop it before the next variant.
            document.querySelectorAll('.card-overflow-menu').forEach((el) => el.remove());
        }

        // The shapes of the five original icons, transcribed from the markup the
        // menu used to concatenate.
        const expected = [
            'path|M8 3v10M3 8h10',
            'path|M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z',
            'rect|2.5,1.5,1.5,7',
            'rect|6,1.5,1.5,7',
            'polygon|3.5,1.5 3.5,8.5 8.5,5',
            'path|M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5',
            'path|M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9',
        ];
        expect([...shapes].sort()).toEqual(expected.sort());
    });
});
