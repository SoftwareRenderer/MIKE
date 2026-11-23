import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import { DOCKER_DEFAULT_CAPABILITIES } from '$lib/components/SandboxConfigModal/capabilities';
import type { Project, Task } from '$lib/types';

/**
 * The Create extension dialog: a valid form stops at the recommendation instead of creating, what the
 * person opted into is what the create sends — including the answer nobody touched, which must leave the
 * project isolated with no capability — and the shell receives the task it is about to open.
 */

const PROJECT: Project = {
    id: 'sidebar-clock',
    name: 'sidebar-clock',
    path: '/home/tester/.mike/extensions/_dev/sidebar-clock',
    sandbox_enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
} as unknown as Project;

const TASK: Task = {
    id: 'task-1',
    project_id: PROJECT.id,
    title: 'Sidebar clock',
    status: 'in_progress',
} as unknown as Task;

interface Harness {
    /** POST /api/extensions/scaffold calls, in order. */
    scaffolds: Record<string, unknown>[];
    failure: string | null;
}

function newHarness(): Harness {
    return { scaffolds: [], failure: null };
}

function stubFetch(h: Harness) {
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: '/home/tester' });
        if (url.pathname === '/api/extensions/registry') return json([]);
        if (url.pathname === '/api/extensions/local') return json([]);
        if (url.pathname === '/api/extensions/check-updates') return json([]);
        if (url.pathname === '/api/extensions' && (!init?.method || init.method === 'GET')) return json([]);

        if (url.pathname === '/api/extensions/scaffold' && init?.method === 'POST') {
            const body = JSON.parse(String(init.body || '{}'));
            h.scaffolds.push(body);
            if (h.failure) return json({ error: h.failure }, 400);
            return json({ project: PROJECT, extension: { id: 'ext-1', name: 'sidebar-clock' }, task: TASK, repository: PROJECT.path }, 201);
        }

        return json({ error: `unexpected request ${init?.method || 'GET'} ${url.pathname}` }, 404);
    }));
}

function buttons(root: Element): HTMLButtonElement[] {
    return [...root.querySelectorAll('button')] as HTMLButtonElement[];
}

function buttonByText(root: Element, text: string): HTMLButtonElement {
    const found = buttons(root).find((b) => b.textContent?.trim() === text);
    if (!found) throw new Error(`nothing in the dialog is labelled "${text}"`);
    return found;
}

function switches(root: Element): HTMLButtonElement[] {
    return [...root.querySelectorAll('[role="switch"]')] as HTMLButtonElement[];
}

async function renderTab(onOpenCreatedExtension?: (project: Project, task: Task) => void) {
    const utils = render(ExtensionsTab, { onOpenCreatedExtension });
    await waitFor(() => expect(utils.container.querySelector('.extensions-tab')).toBeTruthy());
    return utils;
}

/** Opens the form and fills in both fields. */
async function openAndFill(container: HTMLElement) {
    await fireEvent.click(buttonByText(container, 'Create extension'));
    const name = await waitForElement(container, 'input[name="name"]') as HTMLInputElement;
    const description = container.querySelector('textarea[name="description"]') as HTMLTextAreaElement;
    name.value = 'Sidebar clock';
    await fireEvent.input(name);
    description.value = 'Show how long each task has been sitting in Review.';
    await fireEvent.input(description);
}

async function waitForElement(container: HTMLElement, selector: string): Promise<Element> {
    await waitFor(() => expect(container.querySelector(selector)).toBeTruthy());
    return container.querySelector(selector) as Element;
}

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the create extension recommendation', () => {
    it('stops at the recommendation with both answers off, and creates nothing', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openAndFill(container);

        await fireEvent.click(buttonByText(container, 'Create'));

        await waitForElement(container, '[role="switch"]');
        expect(h.scaffolds).toEqual([]);
        expect(switches(container).map((s) => s.getAttribute('aria-checked'))).toEqual(['false', 'false']);
    });

    it('sends what the person opted into and hands the shell the project with its task', async () => {
        const h = newHarness();
        stubFetch(h);
        const opened: (Project | Task)[][] = [];
        const { container } = await renderTab((project, task) => opened.push([project, task]));
        await openAndFill(container);
        await fireEvent.click(buttonByText(container, 'Create'));
        await waitForElement(container, '[role="switch"]');

        for (const toggle of switches(container)) await fireEvent.click(toggle);
        await fireEvent.click(buttonByText(container, 'Create'));

        await waitFor(() => expect(opened).toHaveLength(1));
        expect(h.scaffolds).toEqual([{
            name: 'Sidebar clock',
            description: 'Show how long each task has been sitting in Review.',
            sandbox_network_isolation: false,
            sandbox_capabilities: [...DOCKER_DEFAULT_CAPABILITIES],
        }]);
        expect(opened[0][0]).toEqual(PROJECT);
        expect(opened[0][1]).toEqual(TASK);
    });

    it('leaves the project isolated when the person declines both', async () => {
        const h = newHarness();
        stubFetch(h);
        const opened: unknown[][] = [];
        const { container } = await renderTab((project, task) => opened.push([project, task]));
        await openAndFill(container);
        await fireEvent.click(buttonByText(container, 'Create'));
        await waitForElement(container, '[role="switch"]');

        await fireEvent.click(buttonByText(container, 'Create'));

        await waitFor(() => expect(opened).toHaveLength(1));
        expect(h.scaffolds[0]).toMatchObject({
            sandbox_network_isolation: true,
            sandbox_capabilities: [],
        });
    });

    it('keeps the recommendation on screen with the server’s own refusal', async () => {
        const h = newHarness();
        h.failure = 'that capability name is not a word';
        stubFetch(h);
        const opened: unknown[][] = [];
        const { container } = await renderTab((project, task) => opened.push([project, task]));
        await openAndFill(container);
        await fireEvent.click(buttonByText(container, 'Create'));
        await waitForElement(container, '[role="switch"]');
        await fireEvent.click(switches(container)[1]);

        await fireEvent.click(buttonByText(container, 'Create'));

        await waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent)
            .toContain('not a word'));
        expect(opened).toEqual([]);
        expect(switches(container).map((s) => s.getAttribute('aria-checked'))).toEqual(['false', 'true']);
    });

    it('returns to the form without creating anything', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openAndFill(container);
        await fireEvent.click(buttonByText(container, 'Create'));
        await waitForElement(container, '[role="switch"]');

        await fireEvent.click(buttonByText(container, 'Back'));

        await waitForElement(container, 'input[name="name"]');
        expect(h.scaffolds).toEqual([]);
        expect((container.querySelector('input[name="name"]') as HTMLInputElement).value)
            .toBe('Sidebar clock');
    });
});
