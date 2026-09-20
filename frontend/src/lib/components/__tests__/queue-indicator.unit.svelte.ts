import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import QueueIndicator from '$lib/components/Chat/QueueIndicator.svelte';

// One line, several possible reasons, most specific wins — and {seconds} comes out filled in.

const base = {
    queuePosition: 0,
    queueEstimatedWait: 0,
    promptProgress: 0,
    retryCount: 0,
    retryError: '',
    isAgentSelected: () => false,
};

function text(props: Record<string, unknown> = {}): string {
    const { container } = render(QueueIndicator, { ...base, ...props });
    return container.textContent ?? '';
}

describe('QueueIndicator lock wait', () => {
    it('says the task itself is still working, with the elapsed time filled in', () => {
        const shown = text({ lockWait: { domain: 'worktree', seconds: 12 } });
        expect(shown).toContain('this task is still working');
        expect(shown).toContain('12s');
        expect(shown).not.toContain('{seconds}');
    });

    it('says a git operation holds the project when the stash lock is the one held', () => {
        const shown = text({ lockWait: { domain: 'stash', seconds: 6 } });
        expect(shown).toContain('another git operation has this project open');
        expect(shown).toContain('6s');
    });

    it('still names a wait for a domain it has never seen', () => {
        const shown = text({ lockWait: { domain: 'refs', seconds: 4 } });
        expect(shown).toContain('the project is busy');
        expect(shown).not.toContain('refs');
    });

    it('names which kind of provider quietness it is, because they need different sentences', () => {
        const prefill = text({ lockWait: { domain: 'model', seconds: 41, phase: 'first_token' } });
        expect(prefill).toContain('processing the prompt');
        expect(prefill).toContain('41s');

        const midAnswer = text({ lockWait: { domain: 'model', seconds: 76, phase: 'mid_response' } });
        expect(midAnswer).toContain('generating quietly');
        expect(midAnswer).toContain('76s');
        expect(midAnswer).not.toContain('busy');
    });

    // The control: a provider frame without a phase says the generic thing rather than guessing.
    it('falls back to the generic wait when a model frame carries no phase', () => {
        const shown = text({ lockWait: { domain: 'model', seconds: 9 } });
        expect(shown).toContain('the project is busy');
    });

    it('outranks the queue position, which is the vaguer of the two answers', () => {
        const shown = text({ lockWait: { domain: 'worktree', seconds: 12 }, queuePosition: 3, queueEstimatedWait: 40 });
        expect(shown).toContain('this task is still working');
        expect(shown).not.toMatch(/position/);
    });

    // The control: without a lock wait the ladder is what it was before this line existed.
    it('falls back to the awaiting line when nothing is held against the user', () => {
        const shown = text();
        expect(shown).toContain('Awaiting');
        expect(shown).not.toContain('busy');
    });
});
