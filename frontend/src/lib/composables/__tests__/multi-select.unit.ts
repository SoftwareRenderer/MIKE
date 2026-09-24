import { describe, it, expect } from 'vitest';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';

describe('MultiSelect', () => {
    it('starts with no selection', () => {
        const ms = new MultiSelect();
        expect(ms.count).toBe(0);
        expect(ms.hasSelection).toBe(false);
    });

    it('toggles a task into selection', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        expect(ms.count).toBe(1);
        expect(ms.hasSelection).toBe(true);
        expect(ms.isSelected('task-1')).toBe(true);
    });

    it('toggles a task out of selection', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        ms.toggle('task-1');
        expect(ms.count).toBe(0);
        expect(ms.isSelected('task-1')).toBe(false);
    });

    it('selects multiple tasks', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        ms.toggle('task-2');
        ms.toggle('task-3');
        expect(ms.count).toBe(3);
        expect(ms.isSelected('task-1')).toBe(true);
        expect(ms.isSelected('task-2')).toBe(true);
        expect(ms.isSelected('task-3')).toBe(true);
    });

    it('adds a task without removing others', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        ms.add('task-2');
        expect(ms.count).toBe(2);
        expect(ms.isSelected('task-1')).toBe(true);
        expect(ms.isSelected('task-2')).toBe(true);
    });

    it('removes a specific task', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        ms.toggle('task-2');
        ms.toggle('task-3');
        ms.remove('task-2');
        expect(ms.count).toBe(2);
        expect(ms.isSelected('task-1')).toBe(true);
        expect(ms.isSelected('task-2')).toBe(false);
        expect(ms.isSelected('task-3')).toBe(true);
    });

    it('clears all selections', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');
        ms.toggle('task-2');
        ms.clear();
        expect(ms.count).toBe(0);
        expect(ms.isSelected('task-1')).toBe(false);
        expect(ms.isSelected('task-2')).toBe(false);
    });

    it('selects all provided IDs', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1'); // existing selection
        ms.selectAll(['task-a', 'task-b', 'task-c']);
        expect(ms.count).toBe(3);
        expect(ms.isSelected('task-1')).toBe(false);
        expect(ms.isSelected('task-a')).toBe(true);
        expect(ms.isSelected('task-b')).toBe(true);
        expect(ms.isSelected('task-c')).toBe(true);
    });

    it('converts selection to array', () => {
        const ms = new MultiSelect();
        ms.toggle('a');
        ms.toggle('b');
        const arr = ms.toArray();
        expect(arr).toHaveLength(2);
        expect(arr).toContain('a');
        expect(arr).toContain('b');
    });

    it('toggling same task twice returns to unselected', () => {
        const ms = new MultiSelect();
        ms.toggle('x');
        expect(ms.count).toBe(1);
        ms.toggle('x');
        expect(ms.count).toBe(0);
        ms.toggle('x');
        expect(ms.count).toBe(1);
    });

    it('add does not duplicate existing task', () => {
        const ms = new MultiSelect();
        ms.add('task-1');
        ms.add('task-1');
        expect(ms.count).toBe(1);
    });

    it('remove on non-existent task is a no-op', () => {
        const ms = new MultiSelect();
        ms.remove('nonexistent');
        expect(ms.count).toBe(0);
    });
});
