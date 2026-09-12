import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// The mock must define everything TaskHeader and the dismiss path take from $lib/stores.
vi.mock('$lib/stores', () => ({
    appStore: { config: null, projects: [{ id: 'p1', sandbox_memory_limit: 1024 }] },
    dialogStore: { showToast: vi.fn() },
    updateTaskInBoard: vi.fn(),
}));

// Polyfill matchMedia for useIsMobile (desktop: never matches)
beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

import TaskHeader from '$lib/components/CardDetail/TaskHeader.svelte';
import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
import type { Task, SandboxMemoryStats } from '$lib/types';

function task(over: Partial<Task> = {}): Task {
    return { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T', ...over } as unknown as Task;
}

function stats(overrides: Partial<SandboxMemoryStats> = {}): SandboxMemoryStats {
    return {
        running: true,
        memory_current_mb: 100,
        memory_limit_mb: 1024,
        memory_peak_mb: 0,
        oom_kills: 0,
        ...overrides,
    };
}

/** A stored dismissal. */
const ack = (over: Partial<{ peak_mb: number; oom_kills: number; limit_mb: number }> = {}) => ({
    peak_mb: 1024, oom_kills: 2, limit_mb: 1024, ...over,
});

function renderHeader(t: Task = task()) {
    return render(TaskHeader, {
        props: {
            workspaceDir: '/tmp/wt',
            taskId: 'task-1',
            sandboxEnabled: true,
            badge: { text: 'In Progress', color: '#0969da' },
            onClose: () => {},
            onOpenTerminal: () => {},
            onOpenExplorer: () => {},
            onDelete: async () => {},
            task: t,
        },
    });
}

function titleOf(container: HTMLElement): string {
    return (container.querySelector('button.sandbox-btn') as HTMLButtonElement).title;
}

describe('TaskHeader sandbox memory dot', () => {
    beforeEach(() => {
        sandboxStatsStore.remove('task-1');
    });

    afterEach(() => {
        sandboxStatsStore.remove('task-1');
        cleanup();
    });

    it('shows no dot when there are no stats yet', () => {
        const { container } = renderHeader();
        expect(container.querySelector('.mem-dot')).toBeNull();
    });

    it('shows no dot for healthy usage', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 100 }));
        // Wait until the store update is reflected (title changed), then assert no dot.
        await vi.waitFor(() => expect(titleOf(container)).toBe('Sandbox enabled · 100 / 1024 MB (click for details)'));
        expect(container.querySelector('.mem-dot')).toBeNull();
    });

    it('warns in orange at 80% of the limit, which is no one\'s action to take', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 1000, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot')).not.toBeNull());
        expect(container.querySelector('.mem-dot-warn')).not.toBeNull();
        expect(container.querySelector('.mem-dot-alert')).toBeNull();
    });

    it('turns the dot red for the alert the user has to act on', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 10, memory_peak_mb: 1024, oom_kills: 2 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot-alert')).not.toBeNull());
    });

    it('falls back to orange once a live-pressure alert is dismissed', async () => {
        const { container } = renderHeader(task({ memory_alert_ack: ack() }));
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 1000, memory_peak_mb: 1024, oom_kills: 2 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot')).not.toBeNull());
        expect(container.querySelector('.mem-dot-warn')).not.toBeNull();
        expect(container.querySelector('.mem-dot-alert')).toBeNull();
    });

    it('shows no dot once a dismissed alert has nothing left to say', async () => {
        const { container } = renderHeader(task({ memory_alert_ack: ack() }));
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 100, memory_peak_mb: 1024, oom_kills: 2 }));
        await vi.waitFor(() => expect(titleOf(container)).toContain('100 / 1024 MB'));
        expect(container.querySelector('.mem-dot')).toBeNull();
    });

    it('shows no dot when the peak is just below the limit and usage is healthy', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 100, memory_peak_mb: 1023, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(titleOf(container)).toBe('Sandbox enabled · 100 / 1024 MB (click for details)'));
        expect(container.querySelector('.mem-dot')).toBeNull();
    });

    it('measures the recorded peak of a finished run against the project limit', async () => {
        // No live stats: the task row is all there is.
        const { container } = renderHeader(task({ sandbox_memory_peak_mb: 1024 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot-alert')).not.toBeNull());
    });

    it('names the live usage in the button title', async () => {
        const { container } = renderHeader();
        expect(titleOf(container)).toBe('Sandbox enabled (click for details)');

        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 412, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(titleOf(container)).toBe('Sandbox enabled · 412 / 1024 MB (click for details)'));
    });

    it('says in the title what the alert is, in the same words as the alert bar', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 10, memory_peak_mb: 1024, oom_kills: 2 }));
        await vi.waitFor(() => expect(titleOf(container)).toBe(
            'Sandbox enabled · 10 / 1024 MB · Peak 1024 MB against a 1024 MB limit · 2 processes were killed (click for details)',
        ));
    });

    it('appends the peak-reached clause to the button title when there is no alert to show', async () => {
        const { container } = renderHeader(task({ memory_alert_ack: ack({ peak_mb: 2048 }) }));
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 10, memory_peak_mb: 2048, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(titleOf(container)).toContain('peak reached limit'));
    });
});
