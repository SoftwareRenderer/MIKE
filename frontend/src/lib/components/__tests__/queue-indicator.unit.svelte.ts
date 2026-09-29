import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import QueueIndicator from '$lib/components/Chat/QueueIndicator.svelte';
import { prioritizeTask } from '$lib/api';

vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    prioritizeTask: vi.fn(async () => false),
}));

// Each render lands in document.body, and the queries a render hands back search that whole element.
afterEach(cleanup);

beforeEach(() => {
    vi.mocked(prioritizeTask).mockReset();
    vi.mocked(prioritizeTask).mockResolvedValue(false);
});

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

// The button that asks to go first. Whether the wait is the one this offers is decided upstream
// (`$lib/utils/queue-priority.ts`); what lives here is that it shows only when told to, and what its
// answer looks like while the request is out, after a yes, and after a no.
describe('QueueIndicator prioritize button', () => {
    const queued = { queuePosition: 3, queueEstimatedWait: 20, canPrioritize: true, projectId: 'p1', taskId: 't1' };
    const LABEL = 'Prioritize this task';

    it('sits beside the position when the wait is a slot wait', () => {
        const { getByRole } = render(QueueIndicator, { ...base, ...queued });
        expect(getByRole('button', { name: LABEL })).toBeTruthy();
    });

    it('is not there when the wait is not a slot wait', () => {
        const { queryByRole } = render(QueueIndicator, { ...base, ...queued, canPrioritize: false });
        expect(queryByRole('button')).toBeNull();
    });

    it('needs the task it would speak for', () => {
        const noIds = render(QueueIndicator, { ...base, ...queued, projectId: '', taskId: '' });
        expect(noIds.queryByRole('button')).toBeNull();
    });

    it('says what the server answered and keeps saying the position it moved', async () => {
        vi.mocked(prioritizeTask).mockResolvedValueOnce(true);
        const { container, getByRole } = render(QueueIndicator, { ...base, ...queued });

        fireEvent.click(getByRole('button', { name: LABEL }));

        await waitFor(() => expect(container.textContent).toContain('Moved to the front of the queue'));
        expect(container.textContent).toContain('position 3');
        expect(container.querySelector('button')).toBeNull();
    });

    it('leaves the button standing when the server says the task is not waiting for a slot', async () => {
        vi.mocked(prioritizeTask).mockResolvedValueOnce(false);
        const { container, getByRole } = render(QueueIndicator, { ...base, ...queued });

        fireEvent.click(getByRole('button', { name: LABEL }));
        await waitFor(() => expect(prioritizeTask).toHaveBeenCalledWith('p1', 't1'));

        expect(container.textContent).not.toContain('Moved to the front');
        expect(getByRole('button', { name: LABEL })).toBeTruthy();
    });

    it('leaves the button standing when the call itself fails', async () => {
        vi.mocked(prioritizeTask).mockRejectedValueOnce(new Error('offline'));
        const { container, getByRole } = render(QueueIndicator, { ...base, ...queued });

        fireEvent.click(getByRole('button', { name: LABEL }));
        await waitFor(() => expect(prioritizeTask).toHaveBeenCalled());

        expect(container.textContent).not.toContain('Moved to the front');
        expect(getByRole('button', { name: LABEL })).toBeTruthy();
    });

    it('is offered again for the next wait, because the answer belongs to one wait', async () => {
        vi.mocked(prioritizeTask).mockResolvedValueOnce(true);
        const { container, getByRole, rerender } = render(QueueIndicator, { ...base, ...queued });

        fireEvent.click(getByRole('button', { name: LABEL }));
        await waitFor(() => expect(container.textContent).toContain('Moved to the front of the queue'));

        // The slot arrived, then a later turn queued this task again.
        await rerender({ ...base, ...queued, queuePosition: 0 });
        await rerender({ ...base, ...queued, queuePosition: 2 });

        expect(getByRole('button', { name: LABEL })).toBeTruthy();
    });
});
