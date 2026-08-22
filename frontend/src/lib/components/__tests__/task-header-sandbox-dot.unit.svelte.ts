import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// TaskHeader reads appStore.config for the terminal tooltip.
vi.mock('$lib/stores', () => ({
    appStore: { config: null },
}));

import TaskHeader from '$lib/components/CardDetail/TaskHeader.svelte';
import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
import type { Task, SandboxMemoryStats } from '$lib/types';

const task = { id: 'task-1', project_id: 'p1', status: 'in_progress', title: 'T' } as unknown as Task;

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

function renderHeader() {
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
            task,
        },
    });
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
        const btn = () => container.querySelector('button.sandbox-btn') as HTMLButtonElement;
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 100 }));
        // Wait until the store update is reflected (title changed), then assert no dot.
        await vi.waitFor(() => expect(btn().title).toBe('Sandbox enabled · 100 / 1024 MB (click for details)'));
        expect(container.querySelector('.mem-dot')).toBeNull();
    });

    it('shows the dot when usage is near the limit', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 1000, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot')).not.toBeNull());
    });

    it('shows the dot on OOM kills even at low usage', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 10, oom_kills: 1 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot')).not.toBeNull());
    });

    it('shows the dot on OOM kills while not running (idle, most recent stats)', async () => {
        const { container } = renderHeader();
        sandboxStatsStore.set('task-1', stats({ running: false, memory_current_mb: 0, oom_kills: 2 }));
        await vi.waitFor(() => expect(container.querySelector('.mem-dot')).not.toBeNull());
    });

    it('updates the button title with live usage and OOM kills', async () => {
        const { container } = renderHeader();
        const btn = () => container.querySelector('button.sandbox-btn') as HTMLButtonElement;

        expect(btn().title).toBe('Sandbox enabled (click for details)');

        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 412, memory_limit_mb: 1024 }));
        await vi.waitFor(() => expect(btn().title).toBe('Sandbox enabled · 412 / 1024 MB (click for details)'));

        sandboxStatsStore.set('task-1', stats({ memory_current_mb: 10, oom_kills: 2 }));
        await vi.waitFor(() => expect(btn().title).toBe('Sandbox enabled · 10 / 1024 MB · 2 OOM kills (click for details)'));
    });
});
