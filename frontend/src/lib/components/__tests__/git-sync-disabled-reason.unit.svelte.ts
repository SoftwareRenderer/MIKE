import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

import GitStatusPanel from '$lib/components/GitStatusPanel.svelte';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

/**
 * Pull and Push disable each other, and a button's accessible name says only what it would do — so the
 * one thing a reader cannot infer, *which* sync is holding the other, is the reason it owes them. A
 * `title` cannot carry it: it needs a hover. Rendered here with the sync request left unresolved,
 * because the reason only exists while the panel stays busy.
 */

function renderPanel() {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    return render(GitStatusPanel, {
        props: { projectId: 'p1', projectPath: '/tmp/p1', onClose: () => {}, isMobile: false },
    });
}

function syncButtons(container: ParentNode): [Element, Element] {
    const [pull, push] = [...container.querySelectorAll('.panel-icon-btn')];
    expect(pull).toBeTruthy();
    expect(push).toBeTruthy();
    return [pull, push];
}

/** The text behind `aria-describedby`, or null when the control describes nothing. */
function describedBy(container: ParentNode, el: Element): string | null {
    const id = el.getAttribute('aria-describedby');
    if (!id) return null;
    return container.querySelector(`#${id}`)?.textContent?.trim() ?? '';
}

describe('GitStatusPanel: a sync button waiting on the other one names it', () => {
    it('names the running pull to both buttons', async () => {
        const { container } = renderPanel();
        const [pull, push] = syncButtons(container);

        await fireEvent.click(pull);

        await waitFor(() => expect(pull.hasAttribute('disabled')).toBe(true));
        expect(push.hasAttribute('disabled')).toBe(true);
        expect(describedBy(container, pull)).toBe('Pull in progress');
        expect(describedBy(container, push)).toBe('Pull in progress');
    });

    it('names the running push to both buttons', async () => {
        const { container } = renderPanel();
        const [pull, push] = syncButtons(container);

        await fireEvent.click(push);

        await waitFor(() => expect(push.hasAttribute('disabled')).toBe(true));
        expect(pull.hasAttribute('disabled')).toBe(true);
        expect(describedBy(container, pull)).toBe('Push in progress');
        expect(describedBy(container, push)).toBe('Push in progress');
    });

    it('describes nothing while neither is running', () => {
        const { container } = renderPanel();
        const [pull, push] = syncButtons(container);

        expect(pull.hasAttribute('disabled')).toBe(false);
        expect(push.hasAttribute('disabled')).toBe(false);
        expect(describedBy(container, pull)).toBeNull();
        expect(describedBy(container, push)).toBeNull();
    });
});
