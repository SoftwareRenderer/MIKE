import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import TaskDefaults from '$lib/components/ConfigModal/TaskDefaults.svelte';

const TOGGLE = 'button[aria-label="Run subtasks in parallel"]';

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function renderDefaults(props: { runSiblingsParallel?: boolean } = {}) {
    const onDirty = vi.fn();
    const utils = render(TaskDefaults, {
        props: { originBranch: 'main', onDirty, ...props },
    });
    return {
        ...utils,
        onDirty,
        toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement,
    };
}

describe('TaskDefaults run-siblings-in-parallel toggle', () => {
    it('renders the toggle, off by default', () => {
        const { toggle } = renderDefaults();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderDefaults({ runSiblingsParallel: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderDefaults();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('is placed above the "Max failed turns" row', () => {
        const { container } = renderDefaults();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxParallel = fields.findIndex((f) => f.textContent?.includes('Run subtasks in parallel'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        expect(idxParallel).toBeGreaterThan(-1);
        expect(idxMaxFailed).toBeGreaterThan(-1);
        expect(idxParallel).toBeLessThan(idxMaxFailed);
    });
});

const RECLAIM_TOGGLE = 'button[aria-label="Release a waiting task sandbox container"]';

describe('TaskDefaults release-idle-sandboxes toggle', () => {
    function renderWithReclaim(props: { reclaimIdleSandbox?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(TaskDefaults, {
            props: { originBranch: 'main', onDirty, ...props },
        });
        return {
            ...utils,
            onDirty,
            toggle: () => utils.container.querySelector(RECLAIM_TOGGLE) as HTMLButtonElement,
        };
    }

    it('renders the toggle, off by default', () => {
        const { toggle } = renderWithReclaim();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderWithReclaim({ reclaimIdleSandbox: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderWithReclaim();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    // The description is where the cost of this toggle is stated.
    it('says what the released container does not keep', () => {
        const { container } = renderWithReclaim();
        const desc = container.querySelector('#toggle-reclaim-sandbox-desc');
        expect(desc?.textContent).toContain('container stops after 5 idle minutes');
        expect(desc?.textContent).toMatch(/installed inside the container/);
        expect((container.querySelector(RECLAIM_TOGGLE) as HTMLButtonElement).getAttribute('aria-describedby'))
            .toBe('toggle-reclaim-sandbox-desc');
    });

    it('is placed below the "Run subtasks in parallel" row', () => {
        const { container } = renderWithReclaim();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxParallel = fields.findIndex((f) => f.textContent?.includes('Run subtasks in parallel'));
        const idxReclaim = fields.findIndex((f) => f.textContent?.includes('Release idle sandboxes'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        expect(idxParallel).toBeGreaterThan(-1);
        expect(idxReclaim).toBeGreaterThan(idxParallel);
        expect(idxReclaim).toBeLessThan(idxMaxFailed);
    });
});

const BREAKER_TOGGLE = 'button[aria-label="Recover from failed turns automatically"]';

describe('TaskDefaults loop-breaker toggle', () => {
    function renderWithBreaker(props: { loopBreaker?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(TaskDefaults, {
            props: { originBranch: 'main', onDirty, ...props },
        });
        return {
            ...utils,
            onDirty,
            toggle: () => utils.container.querySelector(BREAKER_TOGGLE) as HTMLButtonElement,
        };
    }

    it('renders the toggle, off by default', () => {
        const { toggle } = renderWithBreaker();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderWithBreaker({ loopBreaker: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderWithBreaker();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('says what turning it on buys', () => {
        const { container } = renderWithBreaker();
        const desc = container.querySelector('#toggle-loop-breaker-desc');
        expect(desc?.textContent).toBe('Attempts to automatically recover from failed turns');
        expect((container.querySelector(BREAKER_TOGGLE) as HTMLButtonElement).getAttribute('aria-describedby'))
            .toBe('toggle-loop-breaker-desc');
    });

    it('is the row directly under "Max failed turns"', () => {
        const { container } = renderWithBreaker();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        const idxBreaker = fields.findIndex((f) => f.textContent?.includes('Loop breaker'));
        expect(idxMaxFailed).toBeGreaterThan(-1);
        expect(idxBreaker).toBe(idxMaxFailed + 1);
    });
});
