import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Mock the LSP sandbox memory API — the footer fetches it when the tooltip opens.
const fetchMock = vi.fn();
vi.mock('$lib/api/lsp', () => ({
    fetchLSPSandboxMemory: (...args: unknown[]) => fetchMock(...args),
}));

import EditorFooter from '$lib/components/EditorFooter.svelte';
import type { SandboxMemoryStats } from '$lib/types';

function stats(overrides: Partial<SandboxMemoryStats> = {}): SandboxMemoryStats {
    return {
        running: true,
        memory_current_mb: 128,
        memory_limit_mb: 256,
        memory_peak_mb: 0,
        oom_kills: 0,
        ...overrides,
    };
}

function renderFooter(overrides: Record<string, unknown> = {}) {
    return render(EditorFooter, {
        props: {
            cursorLine: 1,
            cursorCol: 1,
            lineCount: 10,
            charCount: 100,
            onFind: () => {},
            onWrapToggle: () => {},
            lspReady: true,
            lspConnecting: false,
            lspError: '',
            lspLanguage: 'Go',
            lspServerName: 'gopls',
            lspServerVersion: 'v0.15.0',
            lspMessage: '',
            lspWorkspace: '/tmp/workspace',
            lspFile: '/tmp/workspace/main.go',
            ...overrides,
        },
    });
}

const lspButton = () => document.querySelector('button[aria-label="LSP status"]');
const memoryRow = () => {
    const rows = Array.from(document.querySelectorAll('.lsp-tooltip-row'));
    return rows.find((r) => r.textContent?.includes('Sandbox Memory')) ?? null;
};
const memoryValue = () => memoryRow()?.querySelector('.lsp-tooltip-value');

async function openTooltip() {
    const button = lspButton();
    if (!button) throw new Error('LSP status button not found');
    button.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

describe('EditorFooter LSP sandbox memory row', () => {
    beforeEach(() => {
        cleanup();
        fetchMock.mockReset();
    });

    it('does not show the memory row while the tooltip is closed and does not fetch', async () => {
        fetchMock.mockResolvedValue(stats());
        renderFooter();
        expect(memoryRow()).toBeNull();
        expect(fetchMock).not.toHaveBeenCalled();
    });

    it('fetches once when the tooltip opens, for this editor’s workspace and file', async () => {
        fetchMock.mockResolvedValue(stats({ memory_current_mb: 128, memory_limit_mb: 256 }));
        renderFooter();
        await openTooltip();
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(fetchMock).toHaveBeenCalledWith('/tmp/workspace', '/tmp/workspace/main.go');
        expect(memoryRow()).not.toBeNull();
    });

    it.each([
        ['the usage the container reports', stats({ memory_current_mb: 128, memory_limit_mb: 256 }), '128 / 256 MB'],
        ['no limit to compare against', stats({ memory_current_mb: 42, memory_limit_mb: 0 }), '42 MB (no limit)'],
        ['a sandbox that is not running', stats({ running: false, memory_current_mb: 0 }), 'Not running'],
        ['no answer at all', null, '—'],
    ])('writes %s into the row in one grammar', async (_name, answer, text) => {
        fetchMock.mockResolvedValue(answer);
        renderFooter();
        await openTooltip();
        await vi.waitFor(() => expect(memoryValue()?.textContent).toBe(text));
    });

    it.each([
        ['usage near the limit', stats({ memory_current_mb: 250, memory_limit_mb: 256 }), '250 / 256 MB', true],
        ['an OOM kill at low usage', stats({ memory_current_mb: 10, oom_kills: 1 }), '10 / 256 MB', true],
        ['healthy usage', stats(), '128 / 256 MB', false],
    ])('%s wears the alert class: %s', async (_name, answer, text, warned) => {
        fetchMock.mockResolvedValue(answer);
        renderFooter();
        await openTooltip();
        await vi.waitFor(() => expect(memoryValue()?.textContent).toBe(text));
        expect(memoryValue()?.className.includes('lsp-error')).toBe(warned);
    });

    it('fetches again when the tooltip is closed and reopened', async () => {
        fetchMock.mockResolvedValue(stats());
        renderFooter();
        await openTooltip();
        await openTooltip(); // close
        expect(memoryRow()).toBeNull();
        await openTooltip(); // reopen
        expect(fetchMock).toHaveBeenCalledTimes(2);
        await vi.waitFor(() => expect(memoryValue()?.textContent).toBe('128 / 256 MB'));
    });

    it('does not fetch and hides the row when workspace/file context is missing', async () => {
        fetchMock.mockResolvedValue(stats());
        renderFooter({ lspWorkspace: '', lspFile: '' });
        await openTooltip();
        expect(fetchMock).not.toHaveBeenCalled();
        expect(memoryRow()).toBeNull();
    });
});
