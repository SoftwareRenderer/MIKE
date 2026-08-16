import { describe, it, expect, vi, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { render, cleanup } from '@testing-library/svelte';
import StartPanel from '../Chat/StartPanel.svelte';
import { appStore } from '$lib/stores';

const __dirname = dirname(fileURLToPath(import.meta.url));
const startPanelPath = resolve(__dirname, '../Chat/StartPanel.svelte');
const source = readFileSync(startPanelPath, 'utf-8');

const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';
const WIN_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

afterEach(() => {
    cleanup();
    appStore.config = null;
    vi.restoreAllMocks();
});

function renderPanel(config: unknown = null) {
    appStore.config = config as never;
    const utils = render(StartPanel, {
        props: {
            providers: [],
            selectedModel: '',
            selectedUrl: '',
            selectedReasoningEffort: '',
            operatingMode: 'act',
            isStarting: false,
            disabled: false,
            isAgentSelected: () => false,
            onModelSelect: vi.fn(),
            onOperatingModeToggle: vi.fn(),
            onStartTask: vi.fn(),
        },
    });
    const button = () => utils.container.querySelector('.btn-start-task') as HTMLButtonElement;
    const keyHint = () => button()?.querySelector('kbd.key-hint') as HTMLElement | null;
    return { utils, button, keyHint };
}

describe('StartPanel rendering', () => {
    it('shows Start Task button', () => {
        expect(source).toContain('btn-start-task');
        expect(source).toContain('Start Task');
    });

    it('shows model selector when providers are available', () => {
        expect(source).toContain('providers.length > 0');
        expect(source).toContain('ModelSelector');
    });

    it('shows operating mode toggle when not in agent mode', () => {
        expect(source).toContain('!isAgentSelected()');
        expect(source).toContain('btn-plan-toggle');
    });

    it('shows Plan mode indicator', () => {
        expect(source).toContain('operatingMode === \'plan\'');
        expect(source).toContain('Plan');
    });

    it('shows Act mode indicator', () => {
        expect(source).toContain('Act');
    });

    it('shows Explore mode indicator', () => {
        expect(source).toContain('Explore');
        expect(source).toContain('explore');
    });

    it('disables Start Task button while starting', () => {
        expect(source).toContain('disabled={isStarting || disabled');
        expect(source).toContain('Starting...');
    });

    it('disables Start Task button when no model selected', () => {
        expect(source).toContain('!selectedModel');
    });

    it('shows spinner while starting', () => {
        expect(source).toContain('spinner-inline');
        expect(source).toContain('animation: spin');
    });

    it('has proper aria-label on mode toggle', () => {
        expect(source).toContain('aria-label="Toggle operating mode"');
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
