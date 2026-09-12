import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { t } from '$lib/i18n';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// SandboxPanel fetches the project for the settings list.
vi.mock('$lib/api', () => ({
    getProject: vi.fn().mockResolvedValue({
        id: 'proj-1',
        name: 'Test Project',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
    }),
}));

vi.mock('$lib/stores', () => ({
    appStore: { config: null, projects: [] },
}));

import TaskHeader from '$lib/components/CardDetail/TaskHeader.svelte';
import type { Task } from '$lib/types';

// Drives the useIsMobile() matchMedia stub per test (read at stub-call time).
let mediaMatches = true;

beforeEach(() => {
    mediaMatches = true;
    // Polyfill matchMedia for useIsMobile
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: mediaMatches,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    // Tests may have stubbed navigator.clipboard — drop the own property so
    // the jsdom default is restored.
    delete (navigator as { clipboard?: unknown }).clipboard;
});

const WORKSPACE_DIR = '/home/tester/projects/kanban/.worktrees';
const PROJECT_ID = 'proj-1';
const TASK_ID = 'frost-amber-quartz';
const FULL_PATH = `~/projects/kanban/.worktrees/${PROJECT_ID}/${TASK_ID}`;

function renderHeader() {
    const task = { id: TASK_ID, project_id: PROJECT_ID, status: 'in_progress', title: 'T' } as unknown as Task;
    return render(TaskHeader, {
        props: {
            workspaceDir: WORKSPACE_DIR,
            taskId: TASK_ID,
            sandboxEnabled: true,
            badge: { text: 'In Progress', color: '#f97316' },
            onClose: vi.fn(),
            onOpenTerminal: vi.fn(),
            onOpenExplorer: vi.fn(),
            onDelete: vi.fn().mockResolvedValue(undefined),
            task,
        },
    });
}

/** A browser tap = a bubbling pointerdown followed by a click. */
async function tap(el: Element) {
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    await fireEvent.click(el);
}

describe('TaskHeader path tooltip (mobile)', () => {
    it('hides the tooltip initially', () => {
        const { container } = renderHeader();
        expect(container.querySelector('.path-tooltip')).toBeNull();
    });

    it('shows the full path when the label is tapped', async () => {
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;
        expect(label).toBeTruthy();

        await tap(label);

        const tip = container.querySelector('.path-tooltip');
        expect(tip).toBeTruthy();
        expect(tip!.querySelector('.path-tooltip-text')!.textContent).toBe(FULL_PATH);
        expect(tip!.getAttribute('role')).toBe('tooltip');
        expect(label.getAttribute('aria-describedby')).toBe('task-header-path-tooltip');
    });

    it('copies the full path to the clipboard from the tooltip button', async () => {
        const writeText = vi.fn().mockResolvedValue(undefined);
        Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        const copyBtn = container.querySelector('.path-tooltip-copy') as HTMLButtonElement;
        expect(copyBtn).toBeTruthy();
        expect(copyBtn.textContent).toContain(t('task.header.copy'));

        await fireEvent.click(copyBtn);
        await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(FULL_PATH));
        // Confirmation swaps in, tooltip stays open.
        await vi.waitFor(() => expect(copyBtn.textContent).toContain(t('task.header.copied')));
        expect(container.querySelector('.path-tooltip')).toBeTruthy();
    });

    it('does not dismiss the tooltip when the copy button is tapped', async () => {
        const writeText = vi.fn().mockResolvedValue(undefined);
        Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        const copyBtn = container.querySelector('.path-tooltip-copy') as HTMLButtonElement;

        // Real tap sequence on the button.
        copyBtn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
        await fireEvent.click(copyBtn);
        expect(container.querySelector('.path-tooltip')).toBeTruthy();
        expect(writeText).toHaveBeenCalledWith(FULL_PATH);
    });

    it('falls back to execCommand when the async clipboard API is unavailable', async () => {
        Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
        const execCommand = vi.fn(() => true);
        const originalExec = document.execCommand;
        document.execCommand = execCommand;
        try {
            const { container } = renderHeader();
            const label = container.querySelector('.project-prefix') as HTMLElement;

            await tap(label);
            const copyBtn = container.querySelector('.path-tooltip-copy') as HTMLButtonElement;
            await fireEvent.click(copyBtn);
            await vi.waitFor(() => expect(execCommand).toHaveBeenCalledWith('copy'));
            await vi.waitFor(() => expect(copyBtn.textContent).toContain(t('task.header.copied')));
        } finally {
            document.execCommand = originalExec;
        }
    });

    it('keeps the tooltip open without confirmation when the copy fails', async () => {
        const writeText = vi.fn().mockRejectedValue(new Error('denied'));
        Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        const copyBtn = container.querySelector('.path-tooltip-copy') as HTMLButtonElement;
        await fireEvent.click(copyBtn);
        // Let the rejected promise settle.
        await new Promise((r) => setTimeout(r, 0));
        expect(copyBtn.textContent).toContain(t('task.header.copy'));
        expect(container.querySelector('.path-tooltip')).toBeTruthy();
    });

    it('toggles off when the label is tapped again', async () => {
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        expect(container.querySelector('.path-tooltip')).toBeTruthy();

        // Real tap sequence — the pointerdown must not close the tooltip,
        // otherwise the click would reopen it instead of toggling off.
        await tap(label);
        expect(container.querySelector('.path-tooltip')).toBeNull();
        expect(label.getAttribute('aria-describedby')).toBeNull();
    });

    it('dismisses on an outside pointerdown', async () => {
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        expect(container.querySelector('.path-tooltip')).toBeTruthy();

        // The document listener is a raw (non-Svelte) event, so the state
        // flush is async — wait for the DOM to settle.
        document.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
        await vi.waitFor(() => expect(container.querySelector('.path-tooltip')).toBeNull());
    });

    it('auto-hides after 2.5s', async () => {
        vi.useFakeTimers();
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        expect(container.querySelector('.path-tooltip')).toBeTruthy();

        await vi.advanceTimersByTimeAsync(2500);
        expect(container.querySelector('.path-tooltip')).toBeNull();
    });

    it('does nothing on desktop (the native title tooltip is the desktop affordance)', async () => {
        mediaMatches = false;
        const { container } = renderHeader();
        const label = container.querySelector('.project-prefix') as HTMLElement;

        await tap(label);
        expect(container.querySelector('.path-tooltip')).toBeNull();
    });
});
