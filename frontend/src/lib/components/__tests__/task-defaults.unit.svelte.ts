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
