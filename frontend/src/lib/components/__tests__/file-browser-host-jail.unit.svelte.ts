import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowser from '$lib/components/FileBrowser.svelte';

/**
 * The directory picker takes a path on the SERVER's filesystem, so it is jailed to the home
 * directory plus the declared ones. What it shows of that limit is the server's own refusal,
 * unchanged — it is the only message that names both the roots and the widening variable.
 *
 * `onMount` is a no-op under vitest (see vitest-setup.ts), so each case drives the home button,
 * which goes through the same load.
 */

const HOME = '/home/operator';
const ENV_VAR = 'MIKE_ALLOW_ROOT_FS';
const OUTSIDE = '/etc/ssh';
const REFUSAL = `path is outside the allowed host directories: ${OUTSIDE} (allowed: ${HOME}; set ${ENV_VAR}=1 to allow any host path)`;

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

/** The fs route refuses OUTSIDE. */
function stubServer() {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/home') {
            return json({ home: HOME });
        }
        if (url.pathname === '/api/fs') {
            const path = url.searchParams.get('path') || '';
            if (path.startsWith(OUTSIDE)) return json({ error: REFUSAL }, 403);
            return json({
                path, isGitRepo: false,
                entries: [{ name: 'src', isDir: true, path: `${path}/src`, isGitRepo: false }],
            });
        }
        return json({ error: 'unexpected request' }, 404);
    }));
}

async function openPickerAtHome() {
    const utils = render(FileBrowser, {
        props: { onClose: () => {}, onSelect: () => {}, onClone: async () => {} },
    });
    await fireEvent.click(utils.container.querySelector('.home-btn') as HTMLElement);
    await waitFor(() => expect(utils.container.querySelectorAll('.file-entry').length).toBeGreaterThan(0), { timeout: 2000 });
    return utils;
}

async function goToPath(container: HTMLElement, path: string) {
    const input = container.querySelector('.path-input') as HTMLInputElement;
    input.value = path;
    input.dispatchEvent(new Event('input', { bubbles: true })); // Svelte's bind:value
    await fireEvent.keyDown(input, { key: 'Enter' });
}

describe('FileBrowser — the host directory jail is shown by its refusal', () => {
    afterEach(() => {
        cleanup();
        vi.unstubAllGlobals();
    });

    it('shows the server refusal, unchanged, for a directory outside the jail', async () => {
        stubServer();
        const { container } = await openPickerAtHome();

        await goToPath(container, OUTSIDE);

        await waitFor(() => expect(container.querySelector('.error')).toBeTruthy(), { timeout: 2000 });
        const shown = container.querySelector('.error')?.textContent ?? '';
        expect(shown).toContain(REFUSAL);
        expect(shown).toContain(ENV_VAR);
        expect(container.querySelectorAll('.file-entry').length).toBe(0);
    });
});
