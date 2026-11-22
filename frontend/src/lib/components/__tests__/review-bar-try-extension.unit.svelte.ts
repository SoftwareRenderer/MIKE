import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import ReviewBar from '../Chat/ReviewBar.svelte';
import { t } from '$lib/i18n';

const devRow = { id: 'dev-1', name: 'my-ext' } as any;

function renderBar(props: Record<string, unknown> = {}) {
    const onTryExtension = vi.fn().mockResolvedValue(undefined);
    const utils = render(ReviewBar, {
        props: {
            taskStatus: 'review',
            taskExecutionState: 'idle',
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
            onStartTask: vi.fn().mockResolvedValue(undefined),
            onCommitAndDone: vi.fn().mockResolvedValue(undefined),
            onMoveToDone: vi.fn().mockResolvedValue(undefined),
            onModelSelect: vi.fn().mockResolvedValue(undefined),
            devExtension: devRow,
            onTryExtension,
            ...props,
        },
    });
    const tryButton = () =>
        utils.container.querySelector('.btn-try-extension') as HTMLButtonElement | null;
    const devBar = () =>
        utils.container.querySelector('.chat-review-bar[data-bar="dev"]') as HTMLElement | null;
    return { ...utils, tryButton, devBar, onTryExtension };
}

afterEach(cleanup);

describe('ReviewBar Try Extension', () => {
    it('is offered on the version a run has just finished writing', () => {
        const { devBar, tryButton } = renderBar();
        expect(devBar()).toBeTruthy();
        expect(tryButton()?.textContent?.trim()).toBe(t('chat.review.tryExtension'));
    });

    it('asks nothing of a task still waiting in the backlog', () => {
        expect(renderBar({ taskStatus: 'backlog' }).devBar()).toBeNull();
    });

    it('asks nothing while the turn is still writing the version', () => {
        expect(renderBar({ taskStatus: 'in_progress' }).devBar()).toBeNull();
    });

    it('asks nothing once the version is committed and the task is Done', () => {
        expect(renderBar({ taskStatus: 'done' }).devBar()).toBeNull();
    });

    it('is not offered by a task whose project owns no development row', () => {
        expect(renderBar({ devExtension: null }).devBar()).toBeNull();
    });

    it('loads this task when pressed', async () => {
        const { tryButton, onTryExtension } = renderBar();
        await fireEvent.click(tryButton()!);
        expect(onTryExtension).toHaveBeenCalledTimes(1);
    });

    it('shows the failure text the person carries back to the agent', () => {
        const { devBar } = renderBar({ tryExtensionNotice: 'manifest.json names no entry point' });
        expect(devBar()?.textContent).toContain('manifest.json names no entry point');
    });
});
