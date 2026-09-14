// @vitest-environment jsdom
// The click-outside rule: which press closes, which press does not, and who owns the listener.
// (.svelte.ts extension: the composable under test uses $effect.)
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import Probe from './ClickOutsideProbe.svelte';

/** Renders the probe, opens the box, and returns the close counter the composable drives. */
async function openBox(props: Record<string, unknown> = {}) {
    const onClose = vi.fn();
    const utils = render(Probe, { props: { onClose, ...props } });
    await tick();
    await fireEvent.click(utils.container.querySelector('.toggle')!);
    return { onClose, box: () => utils.container.querySelector('.box') as HTMLElement | null };
}

afterEach(() => {
    cleanup();
    document.body.innerHTML = '';
});

describe('useClickOutside', () => {
    it('closes on a press outside the container', async () => {
        const { onClose, box } = await openBox();
        expect(box()).toBeTruthy();

        await fireEvent.mouseDown(document.body);
        expect(onClose).toHaveBeenCalledTimes(1);
    });

    it('leaves it open when the press lands inside the container', async () => {
        const { onClose } = await openBox();

        await fireEvent.mouseDown(document.querySelector('.box')!);
        await fireEvent.mouseDown(document.querySelector('.inside')!);
        expect(onClose).not.toHaveBeenCalled();
    });

    it('hears nothing while the container is unmounted', async () => {
        const onClose = vi.fn();
        render(Probe, { props: { onClose } });
        await tick();

        await fireEvent.mouseDown(document.body);
        expect(onClose).not.toHaveBeenCalled();
    });

    it('treats an alsoInside region as inside the open thing', async () => {
        const { onClose } = await openBox({ portal: true });

        await fireEvent.mouseDown(document.querySelector('.portal')!);
        expect(onClose).not.toHaveBeenCalled();

        await fireEvent.mouseDown(document.body);
        expect(onClose).toHaveBeenCalledTimes(1);
    });

    it('registers once, so a press after a reopen closes exactly once', async () => {
        const { onClose, box } = await openBox();

        await fireEvent.mouseDown(document.body);
        expect(box()).toBeNull();

        await fireEvent.click(document.querySelector('.toggle')!);
        expect(box()).toBeTruthy();
        await fireEvent.mouseDown(document.body);
        expect(onClose).toHaveBeenCalledTimes(2);
    });

    it('hears only the event the call asked for', async () => {
        const click = await openBox({ event: 'click' });
        await fireEvent.mouseDown(document.body);
        expect(click.onClose).not.toHaveBeenCalled();
        await fireEvent.click(document.body);
        expect(click.onClose).toHaveBeenCalledTimes(1);

        cleanup();

        const press = await openBox({ event: 'mousedown' });
        await fireEvent.click(document.body);
        expect(press.onClose).not.toHaveBeenCalled();
        await fireEvent.mouseDown(document.body);
        expect(press.onClose).toHaveBeenCalledTimes(1);
    });

    it('capture:true beats a target that stops the press', async () => {
        // The TaskHeader case: a control that swallows its own pointerdown must not keep the
        // open thing open, because the dismiss has to happen on the way in, not on the way back.
        const blocker = document.createElement('div');
        document.body.appendChild(blocker);
        blocker.addEventListener('mousedown', (e) => { e.stopPropagation(); });

        const bubbling = await openBox({ capture: false });
        await fireEvent.mouseDown(blocker);
        expect(bubbling.onClose).not.toHaveBeenCalled();

        cleanup();

        const capturing = await openBox({ capture: true });
        await fireEvent.mouseDown(blocker);
        expect(capturing.onClose).toHaveBeenCalledTimes(1);
    });

    it('stops hearing presses once the component is destroyed', async () => {
        const { onClose } = await openBox();

        cleanup();
        await fireEvent.mouseDown(document.body);
        expect(onClose).not.toHaveBeenCalled();
    });
});
