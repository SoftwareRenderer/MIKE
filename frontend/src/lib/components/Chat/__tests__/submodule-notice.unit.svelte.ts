import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

/**
 * The card a run leaves behind when the person's checkout never fetched a submodule its commit names. What
 * it lists comes from the task route — the same reading the refusal came from — and the one button it offers
 * is the click that makes the fetch the person's own.
 */

const mocks = vi.hoisted(() => ({
    getTaskSubmodules: vi.fn(),
    initProjectSubmodules: vi.fn(),
    showToast: vi.fn(),
}));

vi.mock('$lib/api/git', () => ({
    getTaskSubmodules: (...args: any[]) => mocks.getTaskSubmodules(...args),
    initProjectSubmodules: (...args: any[]) => mocks.initProjectSubmodules(...args),
}));
vi.mock('$lib/stores', () => ({ dialogStore: { showToast: (...args: any[]) => mocks.showToast(...args) } }));

const SubmoduleNotice = (await import('$lib/components/Chat/SubmoduleNotice.svelte')).default;

afterEach(() => {
    cleanup();
    vi.clearAllMocks();
});

function open(message = 'the project checkout at /srv/proj has not fetched 1 submodule') {
    return render(SubmoduleNotice, {
        props: { projectId: 'p1', taskId: 't1', message, onDismiss: vi.fn() },
    });
}

const MISSING = { path: 'vendor/lib', url: 'https://example.invalid/lib.git', populated: false, note: 'its host does not resolve' };

function buttons(container: HTMLElement) {
    return Array.from(container.querySelectorAll('button')).filter(b => !b.getAttribute('aria-label'));
}

describe('the notice of an unfetched submodule', () => {
    it('names the path, the address and why a fetch would not bring it down either', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([MISSING]);
        const { container } = open();

        await waitFor(() => expect(container.textContent).toContain('vendor/lib'));
        expect(container.textContent).toContain('https://example.invalid/lib.git');
        expect(container.textContent).toContain('its host does not resolve');
    });

    it('shows the address as text, never as something the person can click into', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([MISSING]);
        const { container } = open();

        await waitFor(() => expect(container.textContent).toContain('vendor/lib'));
        expect(container.querySelector('a[href]')).toBeNull();
    });

    it('lists nothing of its own when the checkout records no submodule, and says so', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([]);
        const { container } = open();

        await waitFor(() => expect(container.textContent).not.toContain('Reading the project'));
        expect(container.querySelectorAll('.submodule-row')).toHaveLength(0);
        expect(container.textContent).toContain('Nothing is recorded in the project');
        // The run's own sentence is the only description left when the listing has none to give.
        expect(container.textContent).toContain('has not fetched 1 submodule');
    });

    it('offers the click only while something is missing', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([{ ...MISSING, populated: true }]);
        const { container } = open();

        await waitFor(() => expect(container.querySelectorAll('.submodule-row')).toHaveLength(1));
        expect(buttons(container)).toHaveLength(0);
    });

    it('asks the project route once and prints what git said about the click', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([MISSING]);
        mocks.initProjectSubmodules.mockResolvedValue({
            populated: ['vendor/lib'],
            reported: ['submodule vendor/lib was fetched from https://example.invalid/lib.git'],
            missing: [],
        });
        const { container } = open();
        await waitFor(() => expect(container.textContent).toContain('vendor/lib'));

        await fireEvent.click(buttons(container)[0]);

        await waitFor(() => expect(mocks.initProjectSubmodules).toHaveBeenCalledWith('p1'));
        await waitFor(() => expect(container.textContent).toContain('was fetched from'));
        // The listing is re-read rather than remembered: the card says what the checkout holds now.
        expect(mocks.getTaskSubmodules).toHaveBeenCalledTimes(2);
    });

    it('keeps a submodule that stayed out on the card after the click', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([MISSING]);
        mocks.initProjectSubmodules.mockResolvedValue({ populated: [], reported: ['submodule vendor/lib was refused: its URL names no host'], missing: [MISSING] });
        const { container } = open();
        await waitFor(() => expect(container.textContent).toContain('vendor/lib'));

        await fireEvent.click(buttons(container)[0]);

        await waitFor(() => expect(container.textContent).toContain('was refused'));
        expect(container.querySelectorAll('.submodule-row')).toHaveLength(1);
        expect(buttons(container)).toHaveLength(1);
    });

    it('reads the task of the project it was given, not the one the notice guessed', async () => {
        mocks.getTaskSubmodules.mockResolvedValue([]);
        open();

        await waitFor(() => expect(mocks.getTaskSubmodules).toHaveBeenCalledWith('p1', 't1'));
    });

    it('says plainly when the listing could not be read, and still offers the click', async () => {
        mocks.getTaskSubmodules.mockRejectedValue(new Error('read the project\'s submodules failed'));
        const { container } = open();

        await waitFor(() => expect(container.querySelector('.notice-error')).not.toBeNull());
        expect(buttons(container)).toHaveLength(1);
    });
});
