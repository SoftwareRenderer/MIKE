// @vitest-environment jsdom
// The @ picker in the task view lists the files of the task's worktree, because those are the files the task
// will edit. The search reaches that directory only when the request carries the task, so a form that asks
// without it would quietly offer the project's own files instead.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

const searchFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return { ...real, searchFsPaths: (params: unknown) => searchFsPaths(params) };
});

import ChatForm from '$lib/components/Chat/ChatForm.svelte';

beforeAll(() => {
    // jsdom has no layout, so the focus handler's call to scrollIntoView has to be replaced.
    Element.prototype.scrollIntoView = vi.fn();
});

beforeEach(() => {
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
    searchFsPaths.mockResolvedValue({ results: [{ path: 'src/app.ts' }] });
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
});

describe('ChatForm @ mention scope', () => {
    it('asks for the task it belongs to, so the list is the task’s worktree', async () => {
        render(ChatForm, {
            props: { projectId: 'proj-1', taskId: 'task-1', onSubmit: vi.fn() },
        });
        const textarea = document.querySelector<HTMLTextAreaElement>('#chat-message-input')!;

        textarea.focus();
        fireEvent.input(textarea, { target: { value: '@src/' } });
        textarea.setSelectionRange(5, 5);

        await waitFor(() => expect(searchFsPaths).toHaveBeenCalled());
        expect(searchFsPaths).toHaveBeenCalledWith(
            expect.objectContaining({ query: 'src/', project: 'proj-1', task: 'task-1' }),
        );
    });
});
