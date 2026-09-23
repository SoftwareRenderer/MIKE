import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import ReviewBar from '../Chat/ReviewBar.svelte';
import { t } from '$lib/i18n';

function renderBar(props: Record<string, unknown> = {}) {
    const onMoveTask = vi.fn();
    const utils = render(ReviewBar, {
        props: {
            taskStatus: 'in_progress',
            taskExecutionState: 'paused',
            showResumeButton: false,
            showRestartButton: false,
            isStarting: false,
            isCommitting: false,
            hasStagedChanges: false,
            queueStatus: null,
            providers: [],
            selectedModel: '',
            selectedUrl: '',
            selectedReasoningEffort: '',
            onMoveTask,
            onStartTask: vi.fn().mockResolvedValue(undefined),
            onCommitAndDone: vi.fn().mockResolvedValue(undefined),
            onMoveToDone: vi.fn().mockResolvedValue(undefined),
            onModelSelect: vi.fn().mockResolvedValue(undefined),
            ...props,
        },
    });
    const bars = () => Array.from(utils.container.querySelectorAll('.chat-review-bar'));
    const moveButton = () =>
        utils.container.querySelector('.move-buttons .btn-move') as HTMLButtonElement | null;
    const resumeButton = () => utils.container.querySelector('.btn-commit-done') as HTMLButtonElement | null;
    return { ...utils, bars, moveButton, resumeButton, onMoveTask };
}

afterEach(cleanup);

describe('ReviewBar paused bar', () => {
    it('offers Backlog beside Resume for a task paused mid-turn', () => {
        const { bars, moveButton, resumeButton } = renderBar();
        expect(bars()).toHaveLength(1);
        expect(moveButton()?.textContent?.trim()).toBe(t('chat.review.moveToBacklog'));
        expect(resumeButton()?.textContent?.trim()).toBe(t('chat.review.resume'));
    });

    it('sits on the left of the bar, where the move buttons always are', () => {
        const { bars, moveButton } = renderBar();
        const bar = bars()[0];
        expect(bar.firstElementChild).toBe(moveButton()?.parentElement);
    });

    it('asks for the backlog column when clicked', async () => {
        const { moveButton, onMoveTask } = renderBar();
        await fireEvent.click(moveButton()!);
        expect(onMoveTask).toHaveBeenCalledWith('backlog');
    });

    it('offers no move when the host cannot move the task', () => {
        const { moveButton, resumeButton } = renderBar({ onMoveTask: undefined });
        expect(moveButton()).toBeNull();
        expect(resumeButton()).toBeTruthy();
    });

    it('says nothing about backlog while the turn is live', () => {
        const { bars } = renderBar({ taskExecutionState: 'running' });
        expect(bars()).toHaveLength(0);
    });
});
