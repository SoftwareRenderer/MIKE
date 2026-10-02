import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { buildMCPServers } from '../mcp-drafts';

// MIKE_RESTRICT_MCP freezes the registry, not the offer: the tab still lists what is registered and says
// the registry is frozen, and a frozen control points at that notice. Add and Test stay on screen and
// refuse; Remove has no read-only meaning, so it is simply not offered. The variable itself is named by
// the 403 a write gets, not by this copy.

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (_key: string, dflt: unknown) => dflt),
    saveAppSetting: vi.fn(async () => {}),
}));

const MCPTab = (await import('$lib/components/ConfigModal/MCPTab.svelte')).default;

const servers = buildMCPServers({
    mcp_servers: [
        { id: 'srv1', name: 'Search', url: 'https://mcp.example.com/mcp', enabled: true, headers: [{ name: 'Authorization' }] },
    ],
});

function open(locked: boolean) {
    return render(MCPTab, {
        props: { servers, locked, onAdd: vi.fn(), onRemove: vi.fn(), onTest: vi.fn(), onDirty: vi.fn() },
    });
}

function buttonByText(container: HTMLElement, label: string): HTMLButtonElement | undefined {
    return Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.trim() === label);
}

const disabled = (el: Element | null) => el !== null && (el as HTMLInputElement).disabled === true;

afterEach(cleanup);

describe('a restricted registry', () => {
    it('announces the freeze and keeps the server listed', () => {
        const { container } = open(true);
        const notice = container.querySelector('[data-testid="mcp-locked"]');
        expect(notice?.getAttribute('role')).toBe('note');
        expect(notice?.textContent?.trim()).toBeTruthy();
        expect(container.querySelector('[role="switch"]')?.getAttribute('aria-describedby'))
            .toBe('mcp-registry-locked');
        expect((container.querySelector('.mcp-name') as HTMLInputElement).value).toBe('Search');
        expect((container.querySelector('.mcp-url') as HTMLInputElement).value).toBe('https://mcp.example.com/mcp');
    });

    it('freezes every control that could change the registry', () => {
        const { container } = open(true);
        expect(buttonByText(container, 'Add server')?.disabled).toBe(true);
        expect(buttonByText(container, 'Test')?.disabled).toBe(true);
        expect(disabled(container.querySelector('.mcp-name'))).toBe(true);
        expect(disabled(container.querySelector('.mcp-url'))).toBe(true);
        expect(disabled(container.querySelector('.mcp-timeout'))).toBe(true);
        expect(disabled(container.querySelector('input[aria-label="Header value"]'))).toBe(true);
        expect(disabled(container.querySelector('[role="switch"]'))).toBe(true);
        expect(container.querySelector('button[aria-label="Remove this header"]')).toBeNull();
        expect(buttonByText(container, 'Remove')).toBeUndefined();
    });

    it('leaves the registry editable when nothing froze it', () => {
        const { container } = open(false);
        expect(container.querySelector('[data-testid="mcp-locked"]')).toBeNull();
        expect(buttonByText(container, 'Add server')?.disabled).toBe(false);
        expect(disabled(container.querySelector('.mcp-url'))).toBe(false);
        expect(container.querySelector('button[aria-label="Remove this header"]')).not.toBeNull();
        expect(buttonByText(container, 'Remove')).toBeDefined();
    });
});
