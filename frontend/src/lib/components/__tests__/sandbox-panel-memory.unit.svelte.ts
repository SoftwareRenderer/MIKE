import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// SandboxPanel fetches the project for the settings list.
vi.mock('$lib/api', () => ({
    getProject: vi.fn().mockResolvedValue({
        id: 'p1',
        name: 'Test Project',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
    }),
}));

import SandboxPanel from '$lib/components/SandboxPanel.svelte';
import { getProject } from '$lib/api';
import type { Task, SandboxMemoryStats } from '$lib/types';

const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;

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

function renderPanel(stats: SandboxMemoryStats | null) {
    return render(SandboxPanel, {
        props: {
            task,
            stats,
            onClose: () => {},
        },
    });
}

// The panel fetches the project for its read-only settings list.
async function memoryRowValue(container: HTMLElement): Promise<string> {
    return vi.waitFor(async () => {
        const row = Array.from(container.querySelectorAll('.setting-row'))
            .find((r) => r.querySelector('.setting-label')?.textContent?.includes('Memory Limit'));
        const value = row?.querySelector('.setting-value')?.textContent?.trim();
        if (!value) throw new Error('memory limit row not found');
        return value;
    });
}

describe('SandboxPanel memory section', () => {
    afterEach(() => {
        cleanup();
    });

    it('hides the memory section when there are no stats', () => {
        const { container } = renderPanel(null);
        expect(container.querySelector('.memory-section')).toBeNull();
    });

    it('shows live usage against the limit with a progress bar', async () => {
        const { container } = renderPanel(stats({ memory_current_mb: 412, memory_limit_mb: 1024 }));
        const value = await vi.waitFor(() => {
            const el = container.querySelector('.memory-value');
            if (!el) throw new Error('not found');
            return el;
        });
        expect(value.textContent).toContain('412 / 1024 MB');
        const fill = container.querySelector('.memory-bar-fill') as HTMLElement;
        expect(fill).not.toBeNull();
        expect(fill.style.width).toBe('40%');
        expect(fill.classList.contains('warn')).toBe(false);
    });

    it('marks the bar as warning at ≥ 80% of the limit', () => {
        const { container } = renderPanel(stats({ memory_current_mb: 1000, memory_limit_mb: 1024 }));
        const fill = container.querySelector('.memory-bar-fill') as HTMLElement;
        expect(fill).not.toBeNull();
        expect(fill.classList.contains('warn')).toBe(true);
    });

    it('shows "no limit" usage without a bar when the limit is unknown', () => {
        const { container } = renderPanel(stats({ memory_current_mb: 412, memory_limit_mb: 0 }));
        const value = container.querySelector('.memory-value');
        expect(value?.textContent).toContain('412 MB (no limit)');
        expect(container.querySelector('.memory-bar')).toBeNull();
    });

    it('shows the not-running state with an empty bar when the limit is known', () => {
        const { container } = renderPanel(stats({ running: false, memory_current_mb: 0 }));
        const value = container.querySelector('.memory-value');
        expect(value?.textContent).toContain('Not running');
        const fill = container.querySelector('.memory-bar-fill') as HTMLElement;
        expect(fill).not.toBeNull();
        expect(fill.style.width).toBe('0%');
        expect(fill.classList.contains('warn')).toBe(false);
        // No peak reported yet → no tick.
        expect(container.querySelector('.memory-bar-peak')).toBeNull();
    });

    it('renders a peak tick at the peak position with a tooltip', () => {
        const { container } = renderPanel(stats({ memory_current_mb: 412, memory_peak_mb: 500, memory_limit_mb: 1024 }));
        const tick = container.querySelector('.memory-bar-peak') as HTMLElement;
        expect(tick).not.toBeNull();
        expect(tick.style.left).toBe('49%');
        expect(tick.title).toBe('Peak 500 MB');
    });

    it('does not render a peak tick when the peak is unknown', () => {
        const { container } = renderPanel(stats());
        expect(container.querySelector('.memory-bar-peak')).toBeNull();
    });

    it('keeps the peak tick visible when not running', () => {
        const { container } = renderPanel(stats({ running: false, memory_current_mb: 0, memory_peak_mb: 800 }));
        const tick = container.querySelector('.memory-bar-peak') as HTMLElement;
        expect(tick).not.toBeNull();
        expect(tick.style.left).toBe('78%');
    });

    it('clamps the peak tick to 100% when the peak reaches the limit', () => {
        const { container } = renderPanel(stats({ memory_peak_mb: 2048, memory_limit_mb: 1024 }));
        const tick = container.querySelector('.memory-bar-peak') as HTMLElement;
        expect(tick?.style.left).toBe('100%');
    });

    it('shows the peak when known', () => {
        const { container } = renderPanel(stats({ memory_peak_mb: 500 }));
        expect(container.querySelector('.memory-peak')?.textContent).toContain('Peak 500 MB');
    });

    it('shows the OOM warning with the kill count', () => {
        const { container } = renderPanel(stats({ oom_kills: 1 }));
        const warn = container.querySelector('.oom-warning');
        expect(warn).not.toBeNull();
        expect(warn?.textContent).toContain('1 process OOM-killed');

        cleanup();
        const plural = renderPanel(stats({ oom_kills: 3 }));
        expect(plural.container.querySelector('.oom-warning')?.textContent).toContain('3 processes OOM-killed');
    });

    it('shows the OOM warning even when not running (most recent stats persist)', () => {
        const { container } = renderPanel(stats({ running: false, memory_current_mb: 0, oom_kills: 1 }));
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
});
