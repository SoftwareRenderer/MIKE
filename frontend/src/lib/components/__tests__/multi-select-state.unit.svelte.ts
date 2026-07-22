import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';

// Mock svelte transitions
vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

import MultiSelectBar from '$lib/components/MultiSelectBar.svelte';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
import type { Task } from '$lib/types';

// Mock the dialogStore
vi.mock('$lib/stores', () => ({
    dialogStore: {
        showConfirm: vi.fn().mockResolvedValue(true),
        showToast: vi.fn(),
    },
    uiStore: {},
    appStore: {
        config: {
            providers: [
                {
                    name: 'test-provider',
                    display_name: 'Test Provider',
                    url: 'http://test:8080/v1',
                    models: [{ name: 'test-model' }],
                },
            ],
        },
    },
}));

function createMockTask(id: string, status: string = 'backlog'): Task {
    return {
        id,
        project_id: 'test-project',
        title: `Task ${id}`,
        prompt: '',
        status,
        column_order: 0,
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: true,
        auto_merge: true,
        subtasks_auto_merge: true,
        operating_mode: 'act',
        title_generating: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
    };
}

describe('MultiSelectBar', () => {
    let multiSelect: MultiSelect;
    let tasks: Task[];
    let onClear: Mock;

    beforeEach(() => {
        vi.clearAllMocks();
        multiSelect = new MultiSelect();
        tasks = [
            createMockTask('task-1', 'backlog'),
            createMockTask('task-2', 'backlog'),
            createMockTask('task-3', 'backlog'),
        ];
        onClear = vi.fn();

        // Mock window.dispatchEvent
        vi.spyOn(window, 'dispatchEvent').mockImplementation(vi.fn());
    });

    it('should show action bar when 2+ tasks are selected', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const bar = container.querySelector('.multi-select-bar');
        expect(bar).toBeTruthy();
    });

    it('should show action bar when 1 task is selected', () => {
        multiSelect.toggle('task-1');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const bar = container.querySelector('.multi-select-bar');
        expect(bar).toBeTruthy();
    });

    it('should show Start button when all selected tasks are in Backlog', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const startBtn = container.querySelector('.bar-btn-start');
        expect(startBtn).toBeTruthy();
        expect(startBtn?.textContent).toContain('Start 2 tasks');
    });

    it('should have clear selection button in action bar', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const clearBtn = container.querySelector('.bar-btn-clear');
        expect(clearBtn).toBeTruthy();

        // Click clear
        clearBtn?.dispatchEvent(new MouseEvent('click', { bubbles: true }));

        // onClear should have been called
        expect(onClear).toHaveBeenCalled();
    });

    it('should dispatch multi-select-delete event when delete is clicked', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const deleteBtn = container.querySelector('.bar-btn-delete');
        expect(deleteBtn).toBeTruthy();

        // Click delete
        deleteBtn?.dispatchEvent(new MouseEvent('click', { bubbles: true }));

        // A custom event should have been dispatched
        expect(window.dispatchEvent).toHaveBeenCalled();
    });

    it('should show Model button when all selected tasks are in Backlog', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const modelBtn = container.querySelector('.bar-btn-model');
        expect(modelBtn).toBeTruthy();
        expect(modelBtn?.textContent).toContain('Model');
    });

    it('should not show Model button when selected tasks are not all in Backlog', () => {
        const mixedTasks = [
            createMockTask('task-1', 'backlog'),
            createMockTask('task-2', 'in_progress'),
        ];
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks: mixedTasks,
            projectId: 'test-project',
            onClear,
        });

        const modelBtn = container.querySelector('.bar-btn-model');
        expect(modelBtn).toBeNull();
    });

    it('should render Model button with correct aria-label', () => {
        multiSelect.toggle('task-1');
        multiSelect.toggle('task-2');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const modelBtn = container.querySelector('.bar-btn-model');
        expect(modelBtn).toBeTruthy();
        expect(modelBtn?.getAttribute('aria-label')).toBe('Change model for 2 tasks');
    });

    it('should not show Model button when no tasks are selected', () => {
        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const bar = container.querySelector('.multi-select-bar');
        expect(bar).toBeNull();
    });
});
