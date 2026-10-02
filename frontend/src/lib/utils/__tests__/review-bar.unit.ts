import { describe, it, expect } from 'vitest';
import { offersResume } from '../review-bar';
import type { ChatEntry } from '$lib/types';

// Resume also answers "is the question on screen unanswered?", so both readings live in one pure
// function and a component only reads it.

function entry(over: Partial<ChatEntry> = {}): ChatEntry {
    return { id: 'e1', role: 'user', content: 'count to ten', ...over };
}

function state(over: Partial<Parameters<typeof offersResume>[0]> = {}) {
    return {
        taskStatus: 'review', loading: false, summarizing: false,
        awaitingResponse: false, startingTurn: false, lastEntry: entry(),
        ...over,
    };
}

describe('offersResume', () => {
    it('offers Resume for a question the transcript ends on', () => {
        expect(offersResume(state())).toBe(true);
    });

    it('holds the button off while the send is on the wire', () => {
        // A click here would start a second run of a question that is already running.
        expect(offersResume(state({ awaitingResponse: true }))).toBe(false);
    });

    it('holds the button off through a fork-and-send handoff', () => {
        expect(offersResume(state({ startingTurn: true }))).toBe(false);
        expect(offersResume(state({ startingTurn: true, awaitingResponse: true }))).toBe(false);
    });

    it('offers Resume again for a send the offline queue is holding', () => {
        // Nothing is in flight, so the queued row is the recovery this button is for.
        expect(offersResume(state({ awaitingResponse: true, lastEntry: entry({ queued: true }) }))).toBe(true);
    });

    it('holds it off when the transcript does not end on a question', () => {
        expect(offersResume(state({ lastEntry: entry({ role: 'assistant' }) }))).toBe(false);
        expect(offersResume(state({ lastEntry: undefined }))).toBe(false);
    });

    it('holds it off outside the Review column, while running, and while summarizing', () => {
        expect(offersResume(state({ taskStatus: 'in_progress' }))).toBe(false);
        expect(offersResume(state({ loading: true }))).toBe(false);
        expect(offersResume(state({ summarizing: true }))).toBe(false);
    });
});
