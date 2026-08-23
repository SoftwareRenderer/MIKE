import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock svelte transitions and raw asset imports (not available in jsdom)
vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// Card reads Svelte contexts normally created by App; provide standalone defaults
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

// The mock factory is hoisted above module imports, so everything it needs
// must be defined inside the factory itself.
vi.mock('$lib/stores', () => ({
    appStore: {
        selectedTask: null,
        board: null,
        config: {
            providers: [
                {
                    id: 1,
                    name: 'Test Provider',
                    url: 'http://neptune:8010/v1',
                    kind: 'llm',
                    models: [{ name: 'gpt-test' }],
                },
            ],
            worker_provider: '',
            worker_model: '',
        },
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
import { appStore } from '$lib/stores';
import type { Config, Task } from '$lib/types';

let baseConfig: Config;

beforeAll(() => {
    // Captured after imports — the mock is initialized by then.
    baseConfig = appStore.config as Config;
});

afterEach(() => {
    cleanup();
    // Restore the unhidden config so tests are isolated.
    appStore.config = baseConfig;
});

function makeTask(overrides: Partial<Task> = {}): Task {
    return {
        id: 'mb-1',
        project_id: 'test-project',
        title: 'Model badge task',
        prompt: 'do something',
        status: 'backlog',
        column_order: 0,
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
        ...overrides,
    } as Task;
}

function renderCard(task = makeTask(), columnId = 'backlog') {
    const { container } = render(Card, {
        task,
        columnId,
        projectId: 'test-project',
        childrenMap: new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: false,
        onSelect: vi.fn(),
        parentSelected: false,
        showSubtaskBorder: false,
        onRefreshBoard: vi.fn(),
        multiSelect: null as never,
    });
    return { container };
}

describe('Card model badge', () => {
    it('shows the model badge when a provider/model is configured', () => {
        const { container } = renderCard();
        const badge = container.querySelector('.model-badge');
        expect(badge).toBeTruthy();
        expect(badge?.textContent).toContain('gpt-test');
    });

    it('hides the model badge when hide_model_badge is set', () => {
        appStore.config = { ...baseConfig, hide_model_badge: true };
        const { container } = renderCard();
        expect(container.querySelector('.model-badge')).toBeNull();
    });

    it('keeps other badges visible when hide_model_badge is set', () => {
        appStore.config = { ...baseConfig, hide_model_badge: true };
        const { container } = renderCard(makeTask({ status: 'in_progress', execution_state: 'idle' }), 'in_progress');
        // The Idle status badge must survive the hide preference.
        expect(container.querySelector('.badge:not(.model-badge)')).toBeTruthy();
    });
});
