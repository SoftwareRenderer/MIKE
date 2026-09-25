// Which task frames reach an open chat, and how many times: the arming rule and one delivery each.
// (.svelte.ts extension: the composable under test registers an $effect.)
import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import Probe from './TaskEventsProbe.svelte';
import { taskEventStore } from '$lib/stores/task-events.svelte';

function frame(taskId: string, type: string) {
    return { task_id: taskId, type, data: {} };
}

const countOf = (container: HTMLElement) => Number(container.querySelector('.count')!.textContent);
const typesOf = (container: HTMLElement) => container.querySelector('.types')!.textContent;

/**
 * Every case starts from a frame naming another task: `App.svelte` keeps one dispatch slot for
 * whichever task is open, so a chat always finds somebody else's last frame in it.
 */
async function openChat(armed: boolean, taskId = 't1') {
    taskEventStore.publish(frame('someone-else', 'token'));
    const { container } = render(Probe, { props: { taskId } });
    await tick();
    if (armed) {
        await fireEvent.click(container.querySelector('.arm')!);
        await tick();
    }
    return container as HTMLElement;
}

afterEach(cleanup);

describe('useTaskEvents', () => {
    it('hands over nothing while the chat has not read its history', async () => {
        const container = await openChat(false);
        taskEventStore.publish(frame('t1', 'token'));
        await tick();
        expect(countOf(container)).toBe(0);
    });

    it('arms with the frame published last, the way a subscriber was handed it', async () => {
        taskEventStore.publish(frame('t1', 'tool_call'));
        const { container } = render(Probe, { props: { taskId: 't1' } });
        await tick();

        await fireEvent.click(container.querySelector('.arm')!);
        await tick();
        expect(typesOf(container)).toBe('tool_call/idle');
    });

    it('hands that one frame over once, not once per render the frame causes', async () => {
        taskEventStore.publish(frame('t1', 'token'));
        const { container } = render(Probe, { props: { taskId: 't1' } });
        await tick();

        await fireEvent.click(container.querySelector('.arm')!);
        for (let i = 0; i < 5; i++) await tick();
        expect(countOf(container)).toBe(1);
    });

    it('delivers a frame published after arming, and skips one for another task', async () => {
        const container = await openChat(true);

        taskEventStore.publish(frame('someone-else', 'token'));
        await tick();
        expect(countOf(container)).toBe(0);

        taskEventStore.publish(frame('t1', 'token'));
        await tick();
        expect(countOf(container)).toBe(1);
    });

    it('keeps the order of the frames it delivers', async () => {
        const container = await openChat(true);

        taskEventStore.publish(frame('t1', 'tool_call'));
        await tick();
        taskEventStore.publish(frame('t1', 'tool_result'));
        await tick();
        expect(typesOf(container)).toBe('tool_call/idle,tool_result/open');
    });
});
