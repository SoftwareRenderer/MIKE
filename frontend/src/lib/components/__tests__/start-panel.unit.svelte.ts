import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import StartPanel from '../Chat/StartPanel.svelte';
import { appStore } from '$lib/stores';
import type { Provider } from '$lib/types';

const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';
const WIN_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

const LLM: Provider = {
    id: 1, name: 'Test Provider', url: 'http://neptune:8010/v1', kind: 'llm',
    models: [{ name: 'gpt-test' }], concurrency: 1, max_requests_per_second: 0,
} as unknown as Provider;

afterEach(() => {
    cleanup();
    appStore.config = null;
    vi.restoreAllMocks();
});

function renderPanel(config: unknown = null, overrides: Record<string, unknown> = {}) {
    appStore.config = config as never;
    const onStartTask = vi.fn();
    const onOperatingModeToggle = vi.fn();
    const utils = render(StartPanel, {
        props: {
            providers: [],
            selectedModel: '',
            selectedUrl: '',
            selectedReasoningEffort: '',
            operatingMode: 'act',
            isStarting: false,
            isAgentSelected: () => false,
            onModelSelect: vi.fn(),
            onOperatingModeToggle,
            onStartTask,
            ...overrides,
        },
    });
    const button = () => utils.container.querySelector('.btn-start-task') as HTMLButtonElement;
    const keyHint = () => button()?.querySelector('kbd.key-hint') as HTMLElement | null;
    const modeToggle = () => utils.container.querySelector('.btn-plan-toggle') as HTMLButtonElement | null;
    return { utils, container: utils.container, button, keyHint, modeToggle, onStartTask, onOperatingModeToggle };
}

describe('StartPanel rendering', () => {
    it('offers a Start Task button', () => {
        const { button } = renderPanel();
        expect(button()).toBeTruthy();
        expect(button()?.textContent).toContain('Start Task');
    });

    it('starts the task when the button is clicked', async () => {
        const { button, onStartTask } = renderPanel();
        await fireEvent.click(button());
        expect(onStartTask).toHaveBeenCalledTimes(1);
    });

    it('offers no model selector while there is no provider, and one once there is', () => {
        const none = renderPanel();
        expect(none.container.querySelector('.model-selector')).toBeNull();
        cleanup();

        const one = renderPanel(null, { providers: [LLM], selectedModel: 'gpt-test' });
        expect(one.container.querySelector('.model-selector')).toBeTruthy();
    });

    it('names the current operating mode on the toggle', () => {
        expect(renderPanel(null, { operatingMode: 'act' }).modeToggle()?.textContent).toContain('Act');
        cleanup();
        expect(renderPanel(null, { operatingMode: 'plan' }).modeToggle()?.textContent).toContain('Plan');
        cleanup();
        expect(renderPanel(null, { operatingMode: 'explore' }).modeToggle()?.textContent).toContain('Explore');
    });

    it('switches the operating mode from the toggle', async () => {
        const { modeToggle, onOperatingModeToggle } = renderPanel();
        await fireEvent.click(modeToggle()!);
        expect(onOperatingModeToggle).toHaveBeenCalledTimes(1);
    });

    it('hides the mode toggle for an agent provider, which has no modes', () => {
        const { modeToggle } = renderPanel(null, { isAgentSelected: () => true });
        expect(modeToggle()).toBeNull();
    });

    it('disables the button while starting and says so', () => {
        const { button } = renderPanel(null, { isStarting: true });
        expect(button()?.disabled).toBe(true);
        expect(button()?.textContent).toContain('Starting...');
        expect(button()?.querySelector('.spinner')).toBeTruthy();
    });

    it('disables the button when a provider is configured but no model is chosen', () => {
        expect(renderPanel(null, { providers: [LLM], selectedModel: '' }).button()?.disabled).toBe(true);
        cleanup();
        expect(renderPanel(null, { providers: [LLM], selectedModel: 'gpt-test' }).button()?.disabled).toBe(false);
    });

    });

describe('StartPanel start-task key hint', () => {
    it('shows the platform default keycap: Ctrl+Enter on non-Mac', () => {
        vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WIN_UA);
        const { button, keyHint } = renderPanel();
        expect(keyHint()?.textContent).toBe('Ctrl+Enter');
        expect(button()?.getAttribute('title')).toBe('Start the AI task (Ctrl+Enter)');
    });

    it('shows the platform default keycap: Cmd+Enter on Mac', () => {
        vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(MAC_UA);
        const { keyHint } = renderPanel();
        expect(keyHint()?.textContent).toBe('Cmd+Enter');
    });

    it('shows the custom binding formatted', () => {
        vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WIN_UA);
        const { keyHint, button } = renderPanel({ keyboard_shortcuts: { start_task: 'shift+enter' } });
        expect(keyHint()?.textContent).toBe('Shift+Enter');
        expect(button()?.getAttribute('title')).toBe('Start the AI task (Shift+Enter)');
    });

    it('hides the keycap when the user hid hints (tooltip keeps the shortcut)', () => {
        vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WIN_UA);
        const { button, keyHint } = renderPanel({ keyboard_shortcuts: { hide_hints: true } });
        expect(keyHint()).toBeNull();
        expect(button()?.getAttribute('title')).toBe('Start the AI task (Ctrl+Enter)');
    });

    it('shows no keycap or shortcut in the tooltip when the binding is disabled', () => {
        vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WIN_UA);
        const { button, keyHint } = renderPanel({ keyboard_shortcuts: { start_task: '' } });
        expect(keyHint()).toBeNull();
        expect(button()?.getAttribute('title')).toBe('Start the AI task');
    });
});
