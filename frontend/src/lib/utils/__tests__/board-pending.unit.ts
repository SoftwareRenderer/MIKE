import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import type { Project } from '$lib/types';
import { resetToEnglish } from '$lib/i18n';

import { projectAddPending } from '../board-pending';

/**
 * THE BOARD'S READING OF A PROJECT THAT DOES NOT EXIST YET.
 *
 * One function decides whether the backlog column shows its add button or the bar of the add in
 * flight, and what the bar says. It is the only thing standing between a person and a task created
 * against a project id the server has never seen, so both the shape of the number and the words
 * beside it are pinned here rather than left to whatever the stream last sent.
 */

function row(overrides: Partial<Project> = {}): Project {
    return {
        id: 'clone-pending-1',
        name: 'repo',
        path: '',
        created_at: '',
        updated_at: '',
        ...overrides,
    } as Project;
}

beforeEach(() => resetToEnglish());
afterEach(() => resetToEnglish());

describe('projectAddPending', () => {
    it('says nothing about a project the server has', () => {
        expect(projectAddPending(row())).toBeNull();
        expect(projectAddPending(null)).toBeNull();
        expect(projectAddPending(undefined)).toBeNull();
    });

    it('names the wait when the stream has said no line yet', () => {
        expect(projectAddPending(row({ cloning: true }))).toMatchObject({
            failed: false,
            percent: 0,
            caption: 'Adding project…',
            detail: '',
        });
    });

    it('carries the line the server last said', () => {
        expect(projectAddPending(row({ cloning: true, cloneStatus: 'Receiving objects...' })))
            .toMatchObject({ caption: 'Receiving objects...' });
    });

    it('keeps a percentage a bar can be drawn at', () => {
        expect(projectAddPending(row({ cloning: true, cloneProgress: 42 }))?.percent).toBe(42);
        expect(projectAddPending(row({ cloning: true, cloneProgress: 140 }))?.percent).toBe(100);
        expect(projectAddPending(row({ cloning: true, cloneProgress: -5 }))?.percent).toBe(0);
        expect(projectAddPending(row({ cloning: true, cloneProgress: NaN }))?.percent).toBe(0);
    });

    it('reads a stopped clone as a failure holding what git said', () => {
        const pending = projectAddPending(row({
            cloning: true,
            cloneProgress: 60,
            cloneFailure: { message: 'git clone failed', detail: 'fatal: no such repository' },
        }));
        expect(pending).toMatchObject({
            failed: true,
            percent: 60,
            caption: 'git clone failed',
            detail: 'fatal: no such repository',
        });
    });
});
