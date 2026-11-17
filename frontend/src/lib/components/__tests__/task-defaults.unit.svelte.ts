import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import TaskDefaults from '$lib/components/ConfigModal/TaskDefaults.svelte';

/**
 * The behaviour defaults the Task tab holds. Each is a switch that starts off, reads back what the
 * server stored, and makes the dialog dirty only when the person moves it; what one of them costs, and
 * where it sits among the rows, is its own finding below.
 */

interface ToggleRow {
    /** The switch, found by the name it is announced by. */
    toggle: string;
    /** The prop the modal hydrates the row from. */
    prop: string;
}

const TOGGLES: ToggleRow[] = [
    { toggle: 'button[aria-label="Run subtasks in parallel"]', prop: 'runSiblingsParallel' },
    { toggle: 'button[aria-label="Release a waiting task sandbox container"]', prop: 'reclaimIdleSandbox' },
    { toggle: 'button[aria-label="Recover from failed turns automatically"]', prop: 'loopBreaker' },
];

const RECLAIM_TOGGLE = TOGGLES[1].toggle;
const BREAKER_TOGGLE = TOGGLES[2].toggle;

function renderDefaults(props: Record<string, unknown> = {}) {
    const onDirty = vi.fn();
    const utils = render(TaskDefaults, {
        props: { originBranch: 'main', onDirty, ...props },
    });
    return {
        ...utils,
        onDirty,
        toggle: (selector: string) => utils.container.querySelector(selector) as HTMLButtonElement,
    };
}

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

describe.each(TOGGLES)('the $toggle switch', ({ toggle, prop }) => {
    it('renders the toggle, off by default', () => {
        const { toggle: el } = renderDefaults();
        expect(el(toggle)).toBeTruthy();
        expect(el(toggle)?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle: el } = renderDefaults({ [prop]: true });
        expect(el(toggle)?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle: el, onDirty } = renderDefaults();
        await fireEvent.click(el(toggle)!);
        expect(el(toggle)?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });
});

describe('the row order and the sentences beside the switches', () => {
    it('places "Run subtasks in parallel" above the "Max failed turns" row', () => {
        const { container } = renderDefaults();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxParallel = fields.findIndex((f) => f.textContent?.includes('Run subtasks in parallel'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        expect(idxParallel).toBeGreaterThan(-1);
        expect(idxMaxFailed).toBeGreaterThan(-1);
        expect(idxParallel).toBeLessThan(idxMaxFailed);
    });

    // The description is where the cost of this toggle is stated.
    it('says what the released container does not keep', () => {
        const { container, toggle } = renderDefaults();
        const desc = container.querySelector('#toggle-reclaim-sandbox-desc');
        expect(desc?.textContent).toContain('container stops after 5 idle minutes');
        expect(desc?.textContent).toMatch(/installed inside the container/);
        expect(toggle(RECLAIM_TOGGLE).getAttribute('aria-describedby')).toBe('toggle-reclaim-sandbox-desc');
    });

    it('places "Release idle sandboxes" under "Run subtasks in parallel"', () => {
        const { container } = renderDefaults();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxParallel = fields.findIndex((f) => f.textContent?.includes('Run subtasks in parallel'));
        const idxReclaim = fields.findIndex((f) => f.textContent?.includes('Release idle sandboxes'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        expect(idxParallel).toBeGreaterThan(-1);
        expect(idxReclaim).toBeGreaterThan(idxParallel);
        expect(idxReclaim).toBeLessThan(idxMaxFailed);
    });

    it('says what the loop breaker buys', () => {
        const { container, toggle } = renderDefaults();
        const desc = container.querySelector('#toggle-loop-breaker-desc');
        expect(desc?.textContent)
            .toBe('Attempts to automatically recover from failed turns. WARNING: This can cause unbounded token generation.');
        expect(toggle(BREAKER_TOGGLE).getAttribute('aria-describedby')).toBe('toggle-loop-breaker-desc');
    });

    it('places "Loop breaker" directly under "Max failed turns"', () => {
        const { container } = renderDefaults();
        const fields = Array.from(container.querySelectorAll('.toggle-field'));
        const idxMaxFailed = fields.findIndex((f) => f.textContent?.includes('Max failed turns'));
        const idxBreaker = fields.findIndex((f) => f.textContent?.includes('Loop breaker'));
        expect(idxMaxFailed).toBeGreaterThan(-1);
        expect(idxBreaker).toBe(idxMaxFailed + 1);
    });
});
