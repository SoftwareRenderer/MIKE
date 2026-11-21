import { describe, it, expect } from 'vitest';
import { diffBaseMoved, diffHeadRef, hasAnythingToShow, hasCommittedChanges, hasWorkingChanges } from '../diff-base';
import type { Task } from '$lib/types';
import type { DiffFile, DiffRow } from '$lib/types/diff';

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

const file = (over: Partial<DiffFile>): DiffFile => ({ path: 'docs/logo.png', rows: [], added: 0, removed: 0, ...over });
const row = (variant: 'added' | 'removed'): DiffRow => ({ lineNumber: 1, variant, text: 'x' });

describe('hasWorkingChanges', () => {
    it('keeps a picture the branch committed out of the working sections', () => {
        expect(hasWorkingChanges(file({ isBinary: true, fileSize: 4096, hasCommitted: true }))).toBe(false);
    });

    it('lists a picture the tree changed, though it has no rows to show', () => {
        expect(hasWorkingChanges(file({ isBinary: true, fileSize: 4096, hasWorking: true }))).toBe(true);
    });

    it('lists a picture changed on the branch and again in the tree', () => {
        expect(hasWorkingChanges(file({ isBinary: true, hasWorking: true, hasCommitted: true }))).toBe(true);
    });

    it('lists an oversized text file, which git strips of rows the same way', () => {
        expect(hasWorkingChanges(file({ fileSize: 2 << 20, hasWorking: true }))).toBe(true);
    });

    it('places a text file by its rows, flags or no flags', () => {
        expect(hasWorkingChanges(file({ rows: [row('added')] }))).toBe(true);
        expect(hasWorkingChanges(file({ rows: [], committedRows: [row('added')] }))).toBe(false);
    });
});

describe('hasCommittedChanges', () => {
    it('lists the picture the branch committed, which has no committed rows to list', () => {
        expect(hasCommittedChanges(file({ isBinary: true, fileSize: 4096, hasCommitted: true }))).toBe(true);
    });

    it('keeps a picture only the tree changed out of the committed section', () => {
        expect(hasCommittedChanges(file({ isBinary: true, fileSize: 4096, hasWorking: true }))).toBe(false);
    });

    it('lists a text file by the committed half of its rows', () => {
        expect(hasCommittedChanges(file({ rows: [row('added')], committedRows: [row('added')] }))).toBe(true);
        expect(hasCommittedChanges(file({ rows: [row('added')] }))).toBe(false);
    });
});

describe('hasAnythingToShow', () => {
    it('drops a change with nothing to draw, which would render as an empty file', () => {
        expect(hasAnythingToShow(file({}))).toBe(false);
    });

    it('keeps a file whose only news is its size, the oversized text file included', () => {
        expect(hasAnythingToShow(file({ fileSize: 2 << 20 }))).toBe(true);
    });

    it('keeps a file by its badge when it appeared or went away', () => {
        expect(hasAnythingToShow(file({ isNewFile: true }))).toBe(true);
        expect(hasAnythingToShow(file({ isDeleted: true }))).toBe(true);
    });
});
