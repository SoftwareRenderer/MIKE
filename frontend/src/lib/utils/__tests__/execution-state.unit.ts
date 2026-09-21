import { describe, it, expect } from 'vitest';
import { trustedExecutionState } from '../execution-state';

describe('trustedExecutionState', () => {
    it('answers with the row the board copy has not caught up with', () => {
        expect(trustedExecutionState('running', 'idle', 'idle')).toBe('running');
    });

    it('answers with the board copy once it has moved', () => {
        expect(trustedExecutionState('running', 'running', 'idle')).toBe('idle');
        expect(trustedExecutionState('idle', 'idle', 'running')).toBe('running');
    });

    it('answers with the board copy while no row has been read', () => {
        expect(trustedExecutionState(null, '', 'running')).toBe('running');
        expect(trustedExecutionState('', 'idle', 'idle')).toBe('idle');
    });
});
