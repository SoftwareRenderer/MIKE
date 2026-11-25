// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import DeleteProjectDialog from '$lib/components/DeleteProjectDialog.svelte';
import { dialogStore } from '$lib/stores';

// Svelte 5 effects flush on the microtask queue, so give them a few ticks after a store write.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

// Neither [role="dialog"] nor the footer is unique, so the button is found by its label.
function footerButton(container: HTMLElement, label: string): HTMLButtonElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLButtonElement;
}

const clonedRow = { name: 'Checkout', folder: 'my-repo', path: '/home/dev/my-repo', cloned: true };
const addedRow = { name: 'Existing', folder: 'their-repo', path: '/home/dev/their-repo', cloned: false };

afterEach(() => {
    cleanup();
    dialogStore.deleteProjectSlot.clear();
});

describe('DeleteProjectDialog', () => {
    it('asks nothing about the folder for a project MIKE did not clone', async () => {
        const { container } = render(DeleteProjectDialog);
        const answered = dialogStore.showDeleteProject(addedRow);
        await flush();

        expect(container.querySelector('input[type="checkbox"]')).toBeNull();
        expect(container.textContent).toContain('The project folder itself will NOT be deleted.');

        await fireEvent.click(footerButton(container, 'Remove'));
        await flush();

        expect(dialogStore.deleteProjectSlot.pending).toBeNull();
        await expect(answered).resolves.toEqual({ deleteData: false, confirmName: '' });
    });

    it('holds the destructive answer back until the folder name is typed', async () => {
        const { container } = render(DeleteProjectDialog);
        const answered = dialogStore.showDeleteProject(clonedRow);
        await flush();

        const box = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
        expect(box).toBeTruthy();
        // Without the checkbox the row is deleted alone.
        await fireEvent.click(footerButton(container, 'Remove'));
        await flush();
        await expect(answered).resolves.toEqual({ deleteData: false, confirmName: '' });

        const second = dialogStore.showDeleteProject(clonedRow);
        await flush();
        const boxAgain = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
        expect(boxAgain.checked).toBe(false);
        await fireEvent.click(boxAgain);
        await flush();

        const field = container.querySelector('input[type="text"]') as HTMLInputElement;
        expect(field).toBeTruthy();
        expect(footerButton(container, 'Remove').disabled).toBe(true);

        await fireEvent.input(field, { target: { value: 'my-re' } });
        await flush();
        expect(footerButton(container, 'Remove').disabled).toBe(true);

        await fireEvent.input(field, { target: { value: 'my-repo' } });
        await flush();
        expect(footerButton(container, 'Remove').disabled).toBe(false);

        await fireEvent.click(footerButton(container, 'Remove'));
        await flush();

        expect(dialogStore.deleteProjectSlot.pending).toBeNull();
        await expect(second).resolves.toEqual({ deleteData: true, confirmName: 'my-repo' });
    });

    it('types the name into the answer with Enter, and only when it matches', async () => {
        const { container } = render(DeleteProjectDialog);
        const answered = dialogStore.showDeleteProject(clonedRow);
        await flush();

        await fireEvent.click(container.querySelector('input[type="checkbox"]') as HTMLInputElement);
        await flush();
        const field = container.querySelector('input[type="text"]') as HTMLInputElement;

        await fireEvent.keyDown(field, { key: 'Enter' });
        await flush();
        expect(dialogStore.deleteProjectSlot.pending).not.toBeNull();

        await fireEvent.input(field, { target: { value: 'my-repo' } });
        await fireEvent.keyDown(field, { key: 'Enter' });
        await flush();

        await expect(answered).resolves.toEqual({ deleteData: true, confirmName: 'my-repo' });
    });

    it('resolves null and empties the slot when cancelled', async () => {
        const { container } = render(DeleteProjectDialog);
        const answered = dialogStore.showDeleteProject(clonedRow);
        await flush();

        await fireEvent.click(footerButton(container, 'Cancel'));
        await flush();

        expect(dialogStore.deleteProjectSlot.pending).toBeNull();
        await expect(answered).resolves.toBeNull();
    });
});
