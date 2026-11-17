import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// The panel fetches its project; a dismissal is a write to the task row.
vi.mock('$lib/api', () => ({
    getProject: vi.fn().mockResolvedValue({
        id: 'p1',
        name: 'Test Project',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
    }),
    dismissMemoryAlert: vi.fn().mockResolvedValue({}),
}));

import SandboxPanel from '$lib/components/SandboxPanel.svelte';
import { getProject, dismissMemoryAlert } from '$lib/api';
import type { Task, SandboxMemoryStats } from '$lib/types';

const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;

/** A stored dismissal. */
const ack = (over: Partial<{ peak_mb: number; oom_kills: number; limit_mb: number }> = {}) => ({
    peak_mb: 1024, oom_kills: 2, limit_mb: 1024, ...over,
});

function stats(overrides: Partial<SandboxMemoryStats> = {}): SandboxMemoryStats {
    return {
        running: true,
        memory_current_mb: 412,
        memory_limit_mb: 1024,
        memory_peak_mb: 0,
        oom_kills: 0,
        ...overrides,
    };
}

function renderPanel(stats: SandboxMemoryStats | null, taskOverrides: Partial<Task> = {}) {
    return render(SandboxPanel, {
        props: {
            task: { ...task, ...taskOverrides },
            stats,
            onClose: () => {},
        },
    });
}

// The panel fetches the project for its read-only settings list.
async function memoryRowValue(container: HTMLElement): Promise<string> {
    return vi.waitFor(async () => {
        const row = Array.from(container.querySelectorAll('.sandbox-row'))
            .find((r) => r.querySelector('.sandbox-label')?.textContent?.includes('Memory Limit'));
        const value = row?.querySelector('.sandbox-value')?.textContent?.trim();
        if (!value) throw new Error('memory limit row not found');
        return value;
    });
}

describe('SandboxPanel memory section', () => {
    afterEach(() => {
        vi.mocked(dismissMemoryAlert).mockClear();
        cleanup();
    });

    it('hides the memory section when there are no stats', () => {
        const { container } = renderPanel(null);
        expect(container.querySelector('.memory-section')).toBeNull();
    });

    // The bar is the same figure read four ways: the words, the fill, how far it is filled, and whether
    // the filling is worth a colour. No limit means no bar at all — there is nothing to fill against.
    it.each([
        {
            name: 'live usage against a limit', given: { memory_current_mb: 412, memory_limit_mb: 1024 },
            value: '412 / 1024 MB', bar: true, width: '40%', warn: false,
        },
        {
            name: 'usage at the warning share of the limit', given: { memory_current_mb: 1000, memory_limit_mb: 1024 },
            bar: true, warn: true,
        },
        {
            name: 'usage with no limit set', given: { memory_current_mb: 412, memory_limit_mb: 0 },
            value: '412 MB (no limit)', bar: false,
        },
        {
            name: 'a container that is not running', given: { running: false, memory_current_mb: 0 },
            value: 'Not running', bar: true, width: '0%', warn: false,
        },
    ])('reads $name as one bar', async ({ given, value, bar, width, warn }) => {
        const { container } = renderPanel(stats(given));

        if (value) {
            const shown = await vi.waitFor(() => {
                const el = container.querySelector('.memory-value');
                if (!el) throw new Error('not found');
                return el;
            });
            expect(shown.textContent).toContain(value);
        }
        const fill = container.querySelector('.memory-bar-fill');
        expect(fill !== null).toBe(bar);
        if (!bar) {
            expect(container.querySelector('.memory-bar')).toBeNull();
            return;
        }
        if (width) expect((fill as HTMLElement).style.width).toBe(width);
        if (warn !== undefined) expect(fill!.classList.contains('warn')).toBe(warn);
    });

    // The tick is MIKE's own high-water mark, not the kernel's: it is where the run got to, it survives
    // the usage falling back down, and it cannot be pushed past the limit it is drawn against.
    it.each([
        { name: 'a peak under the limit', given: { memory_current_mb: 412, memory_peak_mb: 500, memory_limit_mb: 1024 }, left: '49%' },
        { name: 'a peak while the container is gone', given: { running: false, memory_current_mb: 0, memory_peak_mb: 800 }, left: '78%' },
        { name: 'a peak above the limit it was capped at', given: { memory_peak_mb: 2048, memory_limit_mb: 1024 }, left: '100%' },
        { name: 'no peak reported yet', given: {}, left: null },
    ])('draws the peak tick of $name at $left', ({ given, left }) => {
        const { container } = renderPanel(stats(given));
        const tick = container.querySelector('.memory-bar-peak') as HTMLElement | null;

        expect(tick !== null).toBe(left !== null);
        if (!tick) return;
        expect(tick.style.left).toBe(left);
        expect(tick.title).toBe(`Held ${given.memory_peak_mb} MB at peak`);
    });

    it.each([
        { name: 'a peak under the limit', given: { memory_peak_mb: 500 }, reached: false },
        { name: 'a peak that reached the limit', given: { memory_current_mb: 412, memory_peak_mb: 1024, memory_limit_mb: 1024 }, reached: true },
        { name: 'a peak just under the limit', given: { memory_peak_mb: 1023, memory_limit_mb: 1024 }, reached: false },
        {
            // Nothing was ever hit: with no --memory set there is no limit to reach.
            name: 'a peak with no limit enforced', given: { memory_current_mb: 999, memory_peak_mb: 999, memory_limit_mb: 0 }, reached: false,
        },
        {
            // memory.peak is a high-water mark for the container's lifetime, so the signal outlives the
            // usage that caused it.
            name: 'a peak the run left behind when it went idle',
            given: { running: false, memory_current_mb: 0, memory_peak_mb: 1024, memory_limit_mb: 1024 }, reached: true,
        },
    ])('$name is reported as $reached', ({ given, reached }) => {
        const { container } = renderPanel(stats(given));
        const peak = container.querySelector('.memory-peak') as HTMLElement;

        expect(peak.textContent).toContain(`Held ${given.memory_peak_mb} MB at peak`);
        expect(peak.classList.contains('reached')).toBe(reached);
    });

    // The alert is what the user acts on; the OOM line under it is the record, which stays.
    describe('the out-of-memory alert', () => {
        it('states the peak, the limit and what to do', async () => {
            const { container } = renderPanel(stats({ memory_current_mb: 400, memory_peak_mb: 1024, memory_limit_mb: 1024 }));
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-alert')) throw new Error('no alert rendered');
            });
            const text = container.querySelector('.memory-alert')!.textContent!;
            expect(text).toContain('ran out of memory');
            expect(text).toContain('Held 1024 MB at its peak, against a 1024 MB limit');
            expect(text).toContain('Raise or remove the limit');
        });

        it('keeps the alert but drops the advice when the peak stayed under the limit', async () => {
            const { container } = renderPanel(stats({ memory_current_mb: 400, memory_peak_mb: 1776, memory_limit_mb: 2048, oom_kills: 5 }));
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-alert')) throw new Error('no alert rendered');
            });
            const text = container.querySelector('.memory-alert')!.textContent!;
            expect(text).toContain('ran out of memory');
            expect(text).toContain('5 processes were killed');
            expect(text).not.toContain('Raise or remove the limit');
        });

        it('keeps the alert but drops the advice when no limit is enforced', async () => {
            const { container } = renderPanel(stats({ memory_current_mb: 400, memory_peak_mb: 1776, memory_limit_mb: 0, oom_kills: 1 }));
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-alert')) throw new Error('no alert rendered');
            });
            const text = container.querySelector('.memory-alert')!.textContent!;
            expect(text).toContain('with no limit enforced');
            expect(text).not.toContain('Raise or remove the limit');
        });

        it('sends the numbers the alert showed, so the dismissal lands on the server', async () => {
            const { container } = renderPanel(stats({ memory_peak_mb: 1024, memory_limit_mb: 1024, oom_kills: 2 }));
            const dismiss = (await vi.waitFor(() => {
                const el = container.querySelector('.memory-alert-dismiss');
                if (!el) throw new Error('no dismiss button');
                return el as HTMLButtonElement;
            }));
            dismiss.click();
            await vi.waitFor(() => expect(dismissMemoryAlert).toHaveBeenCalledWith('p1', 'task-1', {
                peak_mb: 1024, oom_kills: 2, limit_mb: 1024,
            }));
        });

        it('leaves the OOM record behind once the alert is dismissed', async () => {
            const { container } = renderPanel(
                stats({ memory_peak_mb: 1024, memory_limit_mb: 1024, oom_kills: 2 }),
                { memory_alert_ack: ack() },
            );
            await vi.waitFor(() => {
                if (!container.querySelector('.oom-warning')) throw new Error('no OOM record rendered');
            });
            expect(container.querySelector('.memory-alert')).toBeNull();
            expect(container.querySelector('.oom-warning')?.textContent).toContain('2 processes OOM-killed');
        });

        it('comes back when the container came back over what was dismissed', async () => {
            const { container } = renderPanel(
                stats({ memory_peak_mb: 2048, memory_limit_mb: 1024, oom_kills: 3 }),
                { memory_alert_ack: ack() },
            );
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-alert')) throw new Error('no alert rendered');
            });
        });

        it('is not raised by usage that stays under the limit', async () => {
            const { container } = renderPanel(stats({ memory_current_mb: 1000, memory_peak_mb: 1000, memory_limit_mb: 1024 }));
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-value')) throw new Error('not rendered yet');
            });
            expect(container.querySelector('.memory-alert')).toBeNull();
        });
    });

    it('shows the OOM warning with the kill count', () => {
        const acknowledged = { memory_alert_ack: ack({ peak_mb: 0, oom_kills: 1 }) };
        const { container } = renderPanel(stats({ oom_kills: 1 }), acknowledged);
        const warn = container.querySelector('.oom-warning');
        expect(warn).not.toBeNull();
        expect(warn?.textContent).toContain('1 process OOM-killed');

        cleanup();
        const plural = renderPanel(stats({ oom_kills: 3 }), { memory_alert_ack: ack({ peak_mb: 0, oom_kills: 3 }) });
        expect(plural.container.querySelector('.oom-warning')?.textContent).toContain('3 processes OOM-killed');
    });

    it('shows the OOM warning even when not running (most recent stats persist)', () => {
        const { container } = renderPanel(
            stats({ running: false, memory_current_mb: 0, oom_kills: 1 }),
            { memory_alert_ack: ack({ peak_mb: 0, oom_kills: 1 }) },
        );
        expect(container.querySelector('.oom-warning')).not.toBeNull();
        expect(container.querySelector('.memory-value')?.textContent).toContain('Not running');
    });

    it('shows the enforced limit in the settings list', async () => {
        const { container } = renderPanel(stats({ running: false, memory_current_mb: 0 }));
        expect(await memoryRowValue(container)).toBe('1024 MB');
    });

    it('shows "No limit" in the settings list when the project saved a blank limit', async () => {
        vi.mocked(getProject).mockResolvedValueOnce({
            id: 'p1',
            name: 'Test Project',
            sandbox_enabled: true,
            sandbox_network_isolation: false,
            sandbox_memory_limit: 0,
        } as any);
        const { container } = renderPanel(stats({ running: false, memory_current_mb: 0 }));

        expect(await memoryRowValue(container)).toBe('No limit');
    });

    // Once the container is gone no sandbox_stats event will ever arrive for this task again.
    describe('with no live stats (review / done)', () => {
        it('shows the peak recorded when the container was removed', async () => {
            const { container } = renderPanel(null, { status: 'review', sandbox_memory_peak_mb: 742 });
            const peak = await vi.waitFor(() => {
                const el = container.querySelector('.memory-peak');
                if (!el) throw new Error('no peak rendered');
                return el;
            });
            expect(peak.textContent).toContain('Held 742 MB at peak');
            expect(container.querySelector('.memory-value')?.textContent).toContain('Not running');
        });

        it('draws the recorded peak against the limit the settings list names', async () => {
            const { container } = renderPanel(null, { status: 'done', sandbox_memory_peak_mb: 742 });
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-bar-peak')) throw new Error('no tick yet');
            });
            expect((container.querySelector('.memory-bar-peak') as HTMLElement).style.left).toBe('72%');
            expect((container.querySelector('.memory-bar-fill') as HTMLElement).style.width).toBe('0%');
            expect(await memoryRowValue(container)).toBe('1024 MB');
        });

        it('marks a recorded peak that hit its limit', async () => {
            const { container } = renderPanel(null, { status: 'done', sandbox_memory_peak_mb: 1024 });
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-peak')) throw new Error('no peak rendered');
            });
            expect(container.querySelector('.memory-peak')?.classList.contains('reached')).toBe(true);
        });

        it('shows a recorded peak without a bar when the project saved no limit', async () => {
            vi.mocked(getProject).mockResolvedValueOnce({
                id: 'p1',
                name: 'Test Project',
                sandbox_enabled: true,
                sandbox_network_isolation: false,
                sandbox_memory_limit: 0,
            } as any);
            const { container } = renderPanel(null, { status: 'done', sandbox_memory_peak_mb: 742 });
            await vi.waitFor(() => {
                if (!container.querySelector('.memory-peak')) throw new Error('no peak rendered');
            });
            expect(container.querySelector('.memory-bar')).toBeNull();
            expect(container.querySelector('.memory-peak')?.classList.contains('reached')).toBe(false);
        });

        it('hides the memory section for a task that never recorded a peak', async () => {
            const { container } = renderPanel(null, { status: 'done' });
            await memoryRowValue(container); // the project has loaded, so the choice is final
            expect(container.querySelector('.memory-section')).toBeNull();
        });

        it('prefers live stats over the recorded peak while the container runs', () => {
            // A new turn's container has its own peak.
            const { container } = renderPanel(stats({ memory_current_mb: 100 }), { sandbox_memory_peak_mb: 742 });
            expect(container.querySelector('.memory-value')?.textContent).toContain('100 / 1024 MB');
            expect(container.querySelector('.memory-peak')).toBeNull();
        });
    });
});
