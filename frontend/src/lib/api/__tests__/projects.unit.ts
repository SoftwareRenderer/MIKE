import { describe, it, expect } from 'vitest';
import { parseCloneEvent, type CloneEvent } from '../projects';

describe('parseCloneEvent', () => {
    it('parses a status event', () => {
        const ev = parseCloneEvent('{"type":"status","message":"Cloning into /tmp/x..."}');
        expect(ev).toEqual({ type: 'status', message: 'Cloning into /tmp/x...' });
    });

    it('parses a progress event with a percent', () => {
        const ev = parseCloneEvent('{"type":"progress","percent":42}') as CloneEvent;
        expect(ev).toEqual({ type: 'progress', percent: 42 });
    });

    it('parses a done event carrying the project', () => {
        const project = { id: 'p1', name: 'repo', path: '/tmp/repo', created_at: '', updated_at: '' };
        const ev = parseCloneEvent(`{"type":"done","project":${JSON.stringify(project)}}`);
        expect(ev).toEqual({ type: 'done', project });
    });

    it('parses an error event', () => {
        const ev = parseCloneEvent('{"type":"error","message":"git clone failed: x"}');
        expect(ev).toEqual({ type: 'error', message: 'git clone failed: x' });
    });

    it('falls back to a default message for errors without one', () => {
        const ev = parseCloneEvent('{"type":"error"}');
        expect(ev).toEqual({ type: 'error', message: 'clone failed' });
    });

    it('treats a missing percent as 0', () => {
        const ev = parseCloneEvent('{"type":"progress"}');
        expect(ev).toEqual({ type: 'progress', percent: 0 });
    });

    it('returns null for non-JSON frames (e.g. legacy text lines)', () => {
        expect(parseCloneEvent('Building sandbox image...')).toBeNull();
        expect(parseCloneEvent('')).toBeNull();
    });

    it('returns null for malformed JSON', () => {
        expect(parseCloneEvent('{"type":"progress","percent":')).toBeNull();
    });

    it('returns null for unknown event types', () => {
        expect(parseCloneEvent('{"type":"something-else"}')).toBeNull();
    });
});
