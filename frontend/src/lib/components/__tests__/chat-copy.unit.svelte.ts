import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Sub-components of ChatEntries are stubbed exactly as chat-entries-loading does: this suite is
// about the copy ChatEntries itself owns, not about what a message bubble renders.
vi.mock('$lib/components/Chat/ChatMessage.svelte', () => ({ default: () => '<div class="mock-chat-message"></div>' }));
vi.mock('$lib/components/Chat/CheckpointDivider.svelte', () => ({ default: () => '<div class="mock-checkpoint-divider"></div>' }));
// QueueIndicator is NOT stubbed (unlike chat-entries-loading): this file renders it directly.
// ChatEntries only mounts it while awaiting, which none of the cases here do.
vi.mock('$lib/constants', () => ({ SCROLL_BOTTOM_THRESHOLD: 100 }));

// ToolCall's asset/icon imports are stubbed the way tool-call.unit.svelte.ts stubs them.
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));
vi.mock('$lib/assets/icons/shield-green.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/zap.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file-text.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/search.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/edit.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/check-circle.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/question.svg?raw', () => ({ default: '' }));
vi.mock('marked', () => ({ marked: { parse: (text: string) => `<p>${text}</p>` } }));
vi.mock('dompurify', () => ({ default: { sanitize: (html: string) => html } }));
vi.mock('$lib/utils/diff-parser', () => ({ buildModifiedSegments: () => ({ oldSegments: [], newSegments: [] }) }));
vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn() }));

import MessageStats from '$lib/components/Chat/MessageStats.svelte';
import MessageActions from '$lib/components/Chat/MessageActions.svelte';
import QueueIndicator from '$lib/components/Chat/QueueIndicator.svelte';
import ContextUsage from '$lib/components/Chat/ContextUsage.svelte';
import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import ToolCall from '$lib/components/Chat/ToolCall.svelte';
import ModelSelector from '$lib/components/Chat/ModelSelector.svelte';
import ChangeModelModal from '$lib/components/ChangeModelModal.svelte';
import EditForm from '$lib/components/Chat/EditForm.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

/**
 * The English values are the contract, exactly as in the settings batch: every locator in the
 * chat-core / chat-messages / commit-dialog / pause-resume / thread-* / mode-select specs — and
 * the five chat unit suites — select on one of these strings, so a translation that lands on a
 * different English string is a broken build, not a translation choice.
 *
 * These render the real components (no `readFileSync` string matching) and deliberately pin the
 * composites a naive conversion loses: the `{used} / {limit} tokens · {percent}%` tooltip, the
 * `Preparing: {path}` progress line, `Wrote {bytes} to:`, the plural pair behind
 * `1 backlog task` vs `3 backlog tasks`, and the `{count} model(s)` count.
 */

afterEach(cleanup);

const stats = { model_name: 'gpt-test', prompt_tokens: 100, predicted_tokens: 42, prompt_ms: 10, predicted_ms: 1234, duration: 1234 };

describe('MessageStats — stats row copy', () => {
    it('keeps the "X tokens · Ys" shape and the completion-token count', () => {
        const { container } = render(MessageStats, { props: { stats } });
        const items = [...container.querySelectorAll('.stat-item')].map(el => el.textContent);
        expect(items).toEqual(['42 tokens', '1.2s']);
        expect([...container.querySelectorAll('.stat-separator')].map(el => el.textContent)).toEqual(['·', '·']);
    });

    it('shows the model fallback without inventing a token row', () => {
        const { container } = render(MessageStats, { props: { stats: { ...stats, model_name: '', predicted_tokens: 0, duration: 0 } } });
        expect(container.querySelector('.stats-model-name')?.textContent).toBe('model');
        expect(container.querySelectorAll('.stat-item').length).toBe(0);
    });
});

describe('MessageActions — aria-labels the specs click on', () => {
    it('labels the user row copy/edit/delete buttons byte-identically', () => {
        const { container } = render(MessageActions, {
            props: { variant: 'user', timestampDisplay: '', onCopy: () => {}, onFork: () => {}, onDelete: () => {} },
        });
        const buttons = [...container.querySelectorAll('button')];
        expect(buttons.map(b => b.getAttribute('aria-label'))).toEqual([
            'Copy message', 'Start new thread from this message', 'Delete message and all following messages',
        ]);
        expect(buttons.map(b => b.getAttribute('title'))).toEqual([
            'Copy message', 'Edit / new thread', 'Delete message and all following messages',
        ]);
    });

    it('labels the assistant row regenerate action', () => {
        const { container } = render(MessageActions, {
            props: { variant: 'assistant', hasContent: true, isStreaming: false, isOptimistic: false, onRegenerate: () => {}, onDelete: () => {} },
        });
        const regen = container.querySelector('button[aria-label="Regenerate response"]');
        expect(regen?.getAttribute('title')).toBe('Regenerate response');
    });
});

describe('QueueIndicator — awaiting line', () => {
    const props = { promptProgress: 0, retryCount: 0, retryError: '', queueEstimatedWait: 0, isAgentSelected: () => false };
    // The line lives in its own span next to the spinner; reading it beats trimming markup
    // whitespace out of the whole container.
    const line = (container: HTMLElement) =>
        container.querySelector('.awaiting-llm > span:not(.spinner)')?.textContent;

    it('composes position, wait and target into one message', () => {
        const { container } = render(QueueIndicator, { props: { ...props, queuePosition: 3, queueEstimatedWait: 45 } });
        expect(line(container)).toBe('Awaiting LLM provider — position 3 in queue, ~45s');
    });

    it('names the agent as the target and the bare awaiting form', () => {
        const { container } = render(QueueIndicator, { props: { ...props, queuePosition: 0, isAgentSelected: () => true } });
        expect(line(container)).toBe('Awaiting agent...');
    });

    it('keeps the retry line when only an error is known', () => {
        const { container } = render(QueueIndicator, {
            props: { ...props, queuePosition: 0, retryCount: 2, retryError: 'EOF' },
        });
        expect(line(container)).toBe('Connection issue (EOF) — retry 2');
    });
});

describe('ContextUsage — donut tooltip', () => {
    it('builds "{used} / {limit} tokens · {percent}%" plus the summarize hint', () => {
        const { container } = render(ContextUsage, { props: { used: 12000, limit: 32000, sessionCost: 0.0002, onSummarize: () => {} } });
        const btn = container.querySelector('.context-usage');
        expect(btn?.getAttribute('aria-label')).toBe('12.0K / 32.0K tokens · 38%');
        expect(btn?.getAttribute('title')).toBe('12.0K / 32.0K tokens · 38% · click to summarize');
        expect(container.querySelector('.cost-badge')?.getAttribute('title')).toBe('Session cost');
    });

    it('drops the limit and the hint when there is nothing to summarize', () => {
        const { container } = render(ContextUsage, { props: { used: 900, limit: 0, sessionCost: 0 } });
        expect(container.querySelector('.context-usage')?.getAttribute('aria-label')).toBe('900 tokens');
    });
});

describe('ChatEntries — list chrome', () => {
    const baseProps = {
        entries: [] as never[], entryMetas: [], lastEntryIdx: -1, activeThreadId: 'main', threads: [],
        awaitingResponse: false, connected: true, queuePosition: 0, queueEstimatedWait: 0, promptProgress: 0,
        retryCount: 0, retryError: '', isAgentSelected: () => false, taskStatus: 'backlog', taskExecutionState: '',
        projectId: 'p', taskId: 't', onDismissError: vi.fn(), onRetryConnect: vi.fn(), onThreadSwitch: vi.fn(),
        onMsgDelete: vi.fn(), onMsgFork: vi.fn(), onMsgSend: vi.fn(), onRegenerate: vi.fn(), onNavigateTask: vi.fn(),
    };

    it('labels the region and the loading / empty states', () => {
        const { container } = render(ChatEntries, { props: { ...baseProps, historyLoading: true, follow: createChatFollow() } });
        expect(container.querySelector('.chat-messages')?.getAttribute('aria-label')).toBe('Chat messages');
        expect(container.querySelector('.chat-loading p')?.textContent).toBe('Loading conversation…');
        cleanup();

        const empty = render(ChatEntries, { props: { ...baseProps, follow: createChatFollow() } });
        expect(empty.container.querySelector('.chat-empty p')?.textContent).toBe('No messages yet');
        expect(empty.container.querySelector('.chat-empty .hint')?.textContent).toBe('Send a message to start the conversation');
    });

    it('labels the build box and falls back to the merge label', () => {
        const entries = [
            { id: 'b1', role: 'system', content: 't', isStreaming: false, buildLog: { title: 'Building sandbox image from Dockerfile...', lines: [], done: false } },
            { id: 'm1', role: 'merge_resolution', content: 'merged without a label', isStreaming: false },
        ];
        const { container } = render(ChatEntries, { props: { ...baseProps, entries, follow: createChatFollow() } });
        expect(container.querySelector('.build-box-pending')?.textContent).toBe('Building...');
        expect(container.querySelector('.build-box-log')?.getAttribute('aria-label')).toBe('Build output');
        expect(container.querySelector('.merge-resolution-label')?.textContent).toBe('Merge conflicts resolved');
    });
});

describe('ToolCall — status and result copy', () => {
    const baseProps = { expandedArgs: new Set<string>(), editDiffOverrides: {}, onToggleArgs: vi.fn(), onToggleEditDiff: vi.fn() };
    const renderTool = (toolCall: Record<string, unknown>) =>
        render(ToolCall, { props: { ...baseProps, toolCall: { toolId: 'x', toolName: 'bash', args: {}, ...toolCall } } });

    it('interpolates the path into the read_file progress line', () => {
        const { container } = renderTool({ toolName: 'read_file', isPending: true, path: 'src/file.go' });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Preparing: src/file.go');
    });

    it('says "Wrote {bytes} to:" for a finished write', () => {
        const { container } = renderTool({ toolName: 'write_file', isPending: false, success: true, bytesWritten: 2048, path: 'src/a.ts' });
        expect(container.querySelector('.write-result-text')?.textContent).toBe('Wrote 2.0 KB to:');
    });

    it('declines the plural on one backlog task', () => {
        const one = renderTool({ toolName: 'view_tasks', success: true, result: JSON.stringify([{ id: 'a-b-c', title: 'T' }]) });
        expect(one.container.querySelector('.view-tasks-header')?.textContent).toBe('1 backlog task');
        cleanup();
        const many = renderTool({ toolName: 'view_tasks', success: true, result: JSON.stringify([{ id: 'a', title: 'T' }, { id: 'b', title: 'U' }, { id: 'c', title: 'V' }]) });
        expect(many.container.querySelector('.view-tasks-header')?.textContent).toBe('3 backlog tasks');
    });
});

describe('ModelSelector — triggers and model count', () => {
    const providers = [
        { name: 'OpenAI', url: 'https://api.openai.com', api_key: '', models: [{ name: 'gpt-a' }, { name: 'gpt-b' }] },
        { name: 'Local', url: 'http://localhost:8080', api_key: '', models: [{ name: 'llama' }] },
    ] as never[];

    it('keeps the trigger aria-labels the model-selector suite selects on', () => {
        const { container } = render(ModelSelector, { props: { providers, currentModel: '', currentUrl: '', onSelect: vi.fn() } });
        expect(container.querySelector('button[aria-label="Select provider"]')).toBeTruthy();
        expect(container.querySelector('button[aria-label="Select model"]')?.getAttribute('title')).toBe('Select model');
    });

    it('counts models with the plural table', async () => {
        const { container } = render(ModelSelector, { props: { providers, currentModel: '', currentUrl: 'https://api.openai.com', onSelect: vi.fn() } });
        await container.querySelector<HTMLButtonElement>('button[aria-label="Select provider"]')?.click();
        await Promise.resolve();
        const counts = [...container.querySelectorAll('.model-count')].map(el => el.textContent);
        expect(counts).toEqual(['2 models', '1 model']);
    });
});

describe('ChangeModelModal — task count in the title', () => {
    const props = { providers: [] as never[], config: null, onClose: vi.fn() };

    it('singularizes for one task', () => {
        const { container } = render(ChangeModelModal, { props: { ...props, taskCount: 1, taskTitles: ['one'] } });
        expect(container.querySelector('.modal-header h2, .modal h2')?.textContent ?? container.textContent).toContain('Change Model for 1 task');
    });

    it('pluralizes for three tasks', () => {
        const { container } = render(ChangeModelModal, { props: { ...props, taskCount: 3, taskTitles: ['a', 'b', 'c'] } });
        expect(container.textContent).toContain('Change Model for 3 tasks');
        expect(container.textContent).toContain('No providers configured');
    });
});

describe('EditForm — submit label per context', () => {
    it('offers "New Thread" when editing a message in the middle of the thread', () => {
        const { container } = render(EditForm, { props: { content: 'hi', isFirstUserMessage: false, isLastUserMessage: false, taskStatus: 'in_progress' } });
        const submit = container.querySelector('.btn-edit-submit');
        expect(submit?.textContent?.trim()).toBe('New Thread');
        expect(submit?.getAttribute('title')).toBe('New Thread');
        expect(container.querySelector('.btn-edit-cancel')?.textContent).toBe('Cancel');
    });

    it('offers "Send" for the last message and "Save" for a backlog start', () => {
        const last = render(EditForm, { props: { content: 'hi', isFirstUserMessage: false, isLastUserMessage: true, taskStatus: 'in_progress' } });
        expect(last.container.querySelector('.btn-edit-submit')?.textContent?.trim()).toBe('Send');
        cleanup();
        const backlog = render(EditForm, { props: { content: 'hi', isFirstUserMessage: true, isLastUserMessage: false, taskStatus: 'backlog' } });
        expect(backlog.container.querySelector('.btn-edit-submit')?.textContent?.trim()).toBe('Save');
    });
});
