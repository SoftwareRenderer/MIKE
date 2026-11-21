import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import type { Project } from '$lib/types';
import { resetToEnglish } from '$lib/i18n';

import { addInProgress } from '../board-pending';

/**
 * THE BOARD'S READING OF A PROJECT THAT DOES NOT EXIST YET.
 *
 * What breaks if this regresses is a task created against a project id the server has never seen,
 * and a bar that sits at zero because it read the selection instead of the row.
 */

const ID = 'clone-pending-1';

function row(overrides: Partial<Project> = {}): Project {
    return {
        id: ID, name: 'repo', path: '', created_at: '', updated_at: '',
        ...overrides,
    } as Project;
}

function reading(overrides: Partial<Project> = {}) {
    return addInProgress([row({ cloning: true, ...overrides })], ID);
}

beforeEach(() => resetToEnglish());
afterEach(() => resetToEnglish());

describe('addInProgress', () => {
    it('says nothing about a project the server has, or no project at all', () => {
        expect(addInProgress([row()], ID)).toBeNull();
        expect(addInProgress([row({ cloning: true })], 'other')).toBeNull();
        expect(addInProgress([], ID)).toBeNull();
        expect(addInProgress([row({ cloning: true })], undefined)).toBeNull();
    });

    it('names the wait when the stream has said no line yet', () => {
        expect(reading()).toEqual({ failed: false, percent: null, caption: 'Adding project…', history: [], detail: '' });
    });

    it('puts the newest line in the caption and the rest above it, in the order they were said', () => {
        expect(reading({ cloneLog: ['Cloning x into y...', 'Receiving objects...', 'Resolving deltas...'] }))
            .toMatchObject({
                caption: 'Resolving deltas...',
                history: ['Cloning x into y...', 'Receiving objects...'],
            });
    });

    it('shows only the last eight lines, so a talkative stream cannot fill the column', () => {
        const lines = Array.from({ length: 20 }, (_, i) => `line ${i}`);
        const pending = reading({ cloneLog: lines });
        expect(pending?.caption).toBe('line 19');
        expect(pending?.history).toEqual(lines.slice(12, 19));
    });

    it('draws the number only while it can still move', () => {
        expect(reading({ cloneProgress: 1 })?.percent).toBe(1);
        expect(reading({ cloneProgress: 42 })?.percent).toBe(42);
        expect(reading({ cloneProgress: 99 })?.percent).toBe(99);
    });

    it("draws no number where git's count measures nothing", () => {
        // 0 is the remote still counting and compressing; 100 is printed before the deltas, the
        // checkout and the submodules. A value out of range clamps into one of those two.
        expect(reading({ cloneProgress: 0 })?.percent).toBeNull();
        expect(reading({ cloneProgress: 100 })?.percent).toBeNull();
        expect(reading({ cloneProgress: 140 })?.percent).toBeNull();
        expect(reading({ cloneProgress: -5 })?.percent).toBeNull();
        expect(reading({ cloneProgress: NaN })?.percent).toBeNull();
        expect(reading({})?.percent).toBeNull();
    });

    it('keeps the number a stopped add reached, even at the ends of the range', () => {
        const stopped = { message: 'git clone failed', detail: '' };
        expect(reading({ cloneProgress: 0, cloneFailure: stopped })?.percent).toBe(0);
        expect(reading({ cloneProgress: 100, cloneFailure: stopped })?.percent).toBe(100);
    });

    it('follows the row, not the object the selection is holding', () => {
        // The clone advanced its row; the selection still holds the placeholder as it was clicked.
        const stale = row({ cloning: true, cloneProgress: 0 });
        const list = [row({ cloning: true, cloneProgress: 70, cloneLog: ['Receiving objects...'] })];
        expect(stale.cloneProgress).toBe(0);
        expect(addInProgress(list, stale.id)).toMatchObject({ percent: 70, caption: 'Receiving objects...' });
    });

    it('stops once the row is gone, whatever the selection still believes', () => {
        // The clone answered: the list holds the real project under its own id, and the selection is
        // still the placeholder object. The add task button belongs on the column now.
        const placeholder = row({ cloning: true, cloneProgress: 100 });
        expect(addInProgress([{ ...placeholder, id: 'real-1', cloning: undefined }], placeholder.id)).toBeNull();
    });

    it('reads a stopped clone as a failure holding what git said', () => {
        const pending = reading({
            cloneProgress: 60,
            cloneLog: ['Cloning x into y...', 'Fetching submodules...'],
            cloneFailure: { message: 'git clone failed', detail: 'fatal: no such repository' },
        });
        expect(pending).toMatchObject({
            failed: true,
            percent: 60,
            caption: 'git clone failed',
            history: ['Cloning x into y...', 'Fetching submodules...'],
            detail: 'fatal: no such repository',
        });
    });
});
