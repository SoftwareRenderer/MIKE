import { describe, it, expect } from 'vitest';
import { trustedExecutionState, isTaskPaused } from '../execution-state';

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

describe('isTaskPaused', () => {
    it('is true only of the state a stop leaves behind', () => {
        expect(isTaskPaused('paused')).toBe(true);
        expect(isTaskPaused('running')).toBe(false);
        expect(isTaskPaused('idle')).toBe(false);
        // A task the board lists under In Progress while its run is live is the case a `status` check gets
        // backwards; the state alone answers it.
        expect(isTaskPaused('pulling_image')).toBe(false);
    });
});
