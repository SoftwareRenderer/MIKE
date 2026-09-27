import { describe, it, expect } from 'vitest';
import { diffBaseMoved, diffHeadRef } from '../diff-base';
import type { Task } from '$lib/types';

// Which side of the diff the panel drew, and when that stopped being the task's answer.

const task = (over: Partial<Task>): Task => ({ status: 'in_progress', branch: 'mike/x', branch_tip: '', ...over }) as Task;

describe('diffHeadRef', () => {
    it('names the working tree for anything still running', () => {
        expect(diffHeadRef(task({ status: 'in_progress' }))).toBe('');
        expect(diffHeadRef(task({ status: 'review' }))).toBe('');
        expect(diffHeadRef(task({ status: 'backlog' }))).toBe('');
    });

    it('names the captured tip for a done task', () => {
        expect(diffHeadRef(task({ status: 'done', branch_tip: 'abc123' }))).toBe('abc123');
    });

    it('falls back to the branch when the tip was never captured', () => {
        expect(diffHeadRef(task({ status: 'done', branch_tip: '' }))).toBe('mike/x');
    });

    it('names nothing for a done task with neither, which is a diff with no base to show', () => {
        expect(diffHeadRef(task({ status: 'done', branch_tip: '', branch: '' }))).toBe('');
    });
});

describe('diffBaseMoved', () => {
    it('says nothing moved while the task shows what the panel drew', () => {
        expect(diffBaseMoved('', task({ status: 'in_progress' }))).toBe(false);
        expect(diffBaseMoved('abc123', task({ status: 'done', branch_tip: 'abc123' }))).toBe(false);
    });

    it('says the base moved when commit-and-done happened behind the panel', () => {
        // The whole reason the return path asks the server: the panel is still showing the working tree.
        expect(diffBaseMoved('', task({ status: 'done', branch_tip: 'abc123' }))).toBe(true);
    });

    it('says the base moved when the task came back for another turn', () => {
        expect(diffBaseMoved('abc123', task({ status: 'in_progress', branch_tip: 'abc123' }))).toBe(true);
    });
});
