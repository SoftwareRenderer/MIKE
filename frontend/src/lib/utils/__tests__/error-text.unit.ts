import { describe, it, expect } from 'vitest';
import { errorText } from '$lib/utils/error-text';

/** The shape `apiFetch` throws when a request got no answer: a plain `Error` wearing `TypeError`. */
function connectionFailure(): Error {
    const e = new Error('Connection issue... Please try again');
    Object.defineProperty(e, 'name', { value: 'TypeError' });
    return e;
}

describe('errorText', () => {
    it('says a connection failure as its sentence, without the class name in front', () => {
        expect(errorText(connectionFailure(), 'Failed to save')).toBe('Connection issue... Please try again');
    });

    it('keeps a server refusal sentence exactly as it arrived', () => {
        expect(errorText(new Error('path is outside the project'), 'Failed to save')).toBe(
            'path is outside the project',
        );
    });

    it('answers the fallback when the error carries nothing to say', () => {
        expect(errorText(new Error(''), 'Failed to save')).toBe('Failed to save');
        expect(errorText(new Error('   '), 'Failed to save')).toBe('Failed to save');
    });

    it('answers the fallback for a thrown value that is not an error', () => {
        expect(errorText(null, 'Failed to save')).toBe('Failed to save');
        expect(errorText(undefined, 'Failed to save')).toBe('Failed to save');
        expect(errorText('disk full', 'Failed to save')).toBe('disk full');
    });

    it('answers the empty string when no fallback was offered', () => {
        expect(errorText(null)).toBe('');
    });
});
