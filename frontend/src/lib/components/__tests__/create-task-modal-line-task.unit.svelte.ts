import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import CreateTaskModal from '$lib/components/CreateTaskModal.svelte';
import { appStore } from '$lib/stores';

vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

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

const baseProps = { projectId: 'p1', onClose: () => {} };
const LINE_PROMPT = 'src/main.go:10-12\n10: func main() {\n11: \tfmt.Println("hi")\n12: }';

function autoStartToggle(container: HTMLElement) {
    return container.querySelector('.toggle-switch[aria-label="Auto start after creating"]') as HTMLElement | null;
}

describe('CreateTaskModal — line-task variant (gutter line selection)', () => {
    afterEach(() => {
        cleanup();
        appStore.config = null;
    });

    it('renders the single Create button and the auto-start toggle (default on)', async () => {
        const { container, getByRole, queryByRole } = render(CreateTaskModal, {
            props: { ...baseProps, lineTask: true, initialPrompt: LINE_PROMPT },
        });
        expect(getByRole('button', { name: /^Create/ })).toBeTruthy();
        // The old two-button variant must be gone
        expect(queryByRole('button', { name: /Create in background/ })).toBeNull();
        expect(queryByRole('button', { name: /Create and Open Task/ })).toBeNull();
        // One primary button, labelled "Create Task" (same label as the default flow)
        expect(container.querySelector('.modal-footer .btn-primary')?.textContent).toContain('Create Task');
        expect(container.querySelectorAll('.modal-footer .btn-primary')).toHaveLength(1);
        const toggle = await waitFor(() => {
            const t = autoStartToggle(container);
            if (t) return t;
            throw new Error('auto-start toggle not rendered');
        });
        expect(toggle.getAttribute('aria-checked')).toBe('true');
    });

    it('prefills the description with the selected lines (verbatim, incl. leading newlines)', async () => {
        // The explorer prepends '\n\n' so the caret starts above the reference
        const withNewlines = `\n\n${LINE_PROMPT}`;
        const { container } = render(CreateTaskModal, {
            props: { ...baseProps, lineTask: true, initialPrompt: withNewlines },
        });
        const textarea = await waitFor(() => {
            const t = container.querySelector<HTMLTextAreaElement>('#task-description');
            if (t) return t;
            throw new Error('description textarea not rendered');
        });
        expect(textarea.value).toBe(withNewlines);
    });

    it('re-prefills when a new line selection arrives while open', async () => {
        const { container, rerender } = render(CreateTaskModal, {
            props: { ...baseProps, lineTask: true, initialPrompt: LINE_PROMPT },
        });
        const textarea = await waitFor(() => {
            const t = container.querySelector<HTMLTextAreaElement>('#task-description');
            if (t) return t;
            throw new Error('description textarea not rendered');
        });
        expect(textarea.value).toBe(LINE_PROMPT);
        // Live prop change — simulates clicking a different gutter line while
        // the (interactive, floating) modal is open.
        const second = 'src/main.go:20\n20: done()';
        rerender({ initialPrompt: second });
        await waitFor(() => expect(textarea.value).toBe(second));
    });

    it('toggling the auto-start option flips the switch', async () => {
        const { container } = render(CreateTaskModal, {
            props: { ...baseProps, lineTask: true, initialPrompt: LINE_PROMPT },
        });
        const toggle = await waitFor(() => {
            const t = autoStartToggle(container);
            if (t) return t;
            throw new Error('auto-start toggle not rendered');
        });
        toggle.click();
        await waitFor(() => expect(toggle.getAttribute('aria-checked')).toBe('false'));
        // Clicking the text also toggles exactly once (no <label> activation
        // forwarding, and the switch stops propagation so it can't double-toggle)
        container.querySelector('.auto-start-label')?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await waitFor(() => expect(toggle.getAttribute('aria-checked')).toBe('true'));
        toggle.click();
        await waitFor(() => expect(toggle.getAttribute('aria-checked')).toBe('false'));
    });

    it('keeps the single Create Task button and no auto-start toggle in the default variant', async () => {
        const { container, queryByRole } = render(CreateTaskModal, {
            props: { ...baseProps, initialPrompt: LINE_PROMPT },
        });
        expect(queryByRole('button', { name: /Create in background/ })).toBeNull();
        expect(queryByRole('button', { name: /Create and Open Task/ })).toBeNull();
        expect(container.querySelector('.modal-footer .btn-primary')?.textContent).toContain('Create Task');
        expect(autoStartToggle(container)).toBeNull();
    });
});
