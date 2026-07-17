import { describe, it, expect, vi, beforeEach } from 'vitest';
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
    appStore: {},
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
    let onClear: ReturnType<typeof vi.fn>;

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

    it('should hide action bar when only 1 task is selected', () => {
        multiSelect.toggle('task-1');

        const { container } = render(MultiSelectBar, {
            multiSelect,
            tasks,
            projectId: 'test-project',
            onClear,
        });

        const bar = container.querySelector('.multi-select-bar');
        expect(bar).toBeNull();
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
});
