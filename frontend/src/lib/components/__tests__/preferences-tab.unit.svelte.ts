import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup, within } from '@testing-library/svelte';
import PreferencesTab from '$lib/components/ConfigModal/PreferencesTab.svelte';

const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';
const WIN_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function mockUserAgent(ua: string) {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(ua);
}

/** Render the tab with per-row scoped queries (rows share class/aria names). */
function renderTab(props: { createTaskKey?: string; createTaskSubmitKey?: string; openTerminalKey?: string; startTaskKey?: string } = {}) {
    const onDirty = vi.fn();
    const utils = render(PreferencesTab, {
        props: { createTaskKey: undefined, createTaskSubmitKey: undefined, openTerminalKey: undefined, startTaskKey: undefined, onDirty, ...props },
    });
    const row = (id: string) => within(utils.container.querySelector(`[data-shortcut="${id}"]`)!);
    return {
        ...utils,
        onDirty,
        create: row('create_task'),
        submit: row('create_task_submit'),
        terminal: row('open_terminal'),
        start: row('start_task'),
    };
}

describe('PreferencesTab hide-hints toggle', () => {
    const TOGGLE = 'button[aria-label="Hide shortcut hints"]';

    function renderToggleTab(props: { hideHints?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(PreferencesTab, {
            props: { createTaskKey: undefined, createTaskSubmitKey: undefined, hideHints: false, onDirty, ...props },
        });
        return { ...utils, onDirty, toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement };
    }

    it('renders the toggle, off by default', () => {
        const { toggle } = renderToggleTab();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderToggleTab({ hideHints: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderToggleTab();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });
});

describe('PreferencesTab create-task row', () => {
    it('shows the default key "C" when no binding is stored', () => {
        const { create } = renderTab();
        const chip = create.getByRole('button', { name: /create task shortcut/i });
        expect(chip.textContent).toBe('C');
    });

    it('shows a custom binding formatted, with Reset and Clear controls', () => {
        const { create } = renderTab({ createTaskKey: 'shift+k' });
        const chip = create.getByRole('button', { name: /create task shortcut/i });
        expect(chip.textContent).toBe('Shift+K');
        expect(create.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
        expect(create.getByRole('button', { name: 'Clear shortcut' })).toBeTruthy();
    });

    it('shows "None" and no Clear control when the shortcut is disabled', () => {
        const { create } = renderTab({ createTaskKey: '' });
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('None');
        expect(create.queryByRole('button', { name: 'Clear shortcut' })).toBeNull();
        // Reset is available from the disabled state to restore the default.
        expect(create.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
    });

    it('captures a pressed key and assigns it as the new binding', async () => {
        const { create, onDirty } = renderTab();
        const chip = create.getByRole('button', { name: /create task shortcut/i });

        await fireEvent.click(chip);
        expect(chip.textContent).toBe('Press a key...');

        await fireEvent.keyDown(window, { key: 'K', shiftKey: true });

        expect(chip.textContent).toBe('Shift+K');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('cancels capture with Escape without changing the binding', async () => {
        const { create, onDirty } = renderTab();
        const chip = create.getByRole('button', { name: /create task shortcut/i });
        await fireEvent.click(chip);
        expect(chip.textContent).toBe('Press a key...');
        await fireEvent.keyDown(window, { key: 'Escape' });
        expect(chip.textContent).toBe('C');
        expect(onDirty).not.toHaveBeenCalled();
    });

    it('flags invalid presses and keeps capturing', async () => {
        const { create, getByText } = renderTab();
        const chip = create.getByRole('button', { name: /create task shortcut/i });
        await fireEvent.click(chip);

        // F5 without a modifier is not a valid binding.
        await fireEvent.keyDown(window, { key: 'F5' });
        expect(chip.textContent).toBe('Press a key...');
        expect(getByText(/invalid/i)).toBeTruthy();

        // A valid press still assigns.
        await fireEvent.keyDown(window, { key: '5' });
        expect(chip.textContent).toBe('5');
    });

    it('resets to the default binding', async () => {
        const { create, onDirty } = renderTab({ createTaskKey: 'control+k' });
        await fireEvent.click(create.getByRole('button', { name: 'Reset to default' }));
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('C');
        expect(onDirty).toHaveBeenCalledTimes(1);
        expect(create.queryByRole('button', { name: 'Reset to default' })).toBeNull();
    });

    it('clears the shortcut (disables it)', async () => {
        const { create, onDirty } = renderTab();
        await fireEvent.click(create.getByRole('button', { name: 'Clear shortcut' }));
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('None');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('describes its purpose for the board action', () => {
        const { create } = renderTab();
        expect(create.getAllByText(/project board/i).length).toBeGreaterThan(0);
    });
});

describe('PreferencesTab open-terminal row', () => {
    it('shows the default key "Ctrl+`" when no binding is stored', () => {
        const { terminal } = renderTab();
        const chip = terminal.getByRole('button', { name: /open terminal shortcut/i });
        expect(chip.textContent).toBe('Ctrl+`');
    });

    it('shows a custom binding formatted, with Reset and Clear controls', () => {
        const { terminal } = renderTab({ openTerminalKey: 'alt+shift+t' });
        const chip = terminal.getByRole('button', { name: /open terminal shortcut/i });
        expect(chip.textContent).toBe('Alt+Shift+T');
        expect(terminal.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
        expect(terminal.getByRole('button', { name: 'Clear shortcut' })).toBeTruthy();
    });

    it('shows "None" and no Clear control when the shortcut is disabled', () => {
        const { terminal } = renderTab({ openTerminalKey: '' });
        expect(terminal.getByRole('button', { name: /open terminal shortcut/i }).textContent).toBe('None');
        expect(terminal.queryByRole('button', { name: 'Clear shortcut' })).toBeNull();
        expect(terminal.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
    });

    it('captures a combo and assigns it to the open-terminal binding only', async () => {
        const { terminal, create, onDirty } = renderTab();
        const chip = terminal.getByRole('button', { name: /open terminal shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'T', altKey: true, shiftKey: true });
        expect(chip.textContent).toBe('Shift+Alt+T');
        // The other rows are untouched.
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('C');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('rejects the browser-reserved Ctrl+T during capture', async () => {
        const { terminal, getByText, onDirty } = renderTab();
        const chip = terminal.getByRole('button', { name: /open terminal shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'T', ctrlKey: true });
        // Still capturing, flagged invalid, binding unchanged.
        expect(chip.textContent).toBe('Press a key...');
        expect(getByText(/invalid/i)).toBeTruthy();
        expect(onDirty).not.toHaveBeenCalled();
    });

    it('resets to the default binding', async () => {
        const { terminal, onDirty } = renderTab({ openTerminalKey: 'shift+alt+t' });
        await fireEvent.click(terminal.getByRole('button', { name: 'Reset to default' }));
        expect(terminal.getByRole('button', { name: /open terminal shortcut/i }).textContent).toBe('Ctrl+`');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('clears the shortcut (disables it)', async () => {
        const { terminal, onDirty } = renderTab();
        await fireEvent.click(terminal.getByRole('button', { name: 'Clear shortcut' }));
        expect(terminal.getByRole('button', { name: /open terminal shortcut/i }).textContent).toBe('None');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('describes its purpose for the terminal panel', () => {
        const { terminal } = renderTab();
        expect(terminal.getAllByText(/terminal panel/i).length).toBeGreaterThan(0);
    });
});

describe('PreferencesTab submit row', () => {
    it('shows the platform default: Ctrl+Enter on non-Mac', () => {
        mockUserAgent(WIN_UA);
        const { submit } = renderTab();
        expect(submit.getByRole('button', { name: /create task \(submit\) shortcut/i }).textContent).toBe('Ctrl+Enter');
    });

    it('shows the platform default: Cmd+Enter on Mac', () => {
        mockUserAgent(MAC_UA);
        const { submit } = renderTab();
        expect(submit.getByRole('button', { name: /create task \(submit\) shortcut/i }).textContent).toBe('Cmd+Enter');
    });

    it('shows a custom binding formatted, with Reset and Clear controls', () => {
        mockUserAgent(WIN_UA);
        const { submit } = renderTab({ createTaskSubmitKey: 'shift+enter' });
        const chip = submit.getByRole('button', { name: /create task \(submit\) shortcut/i });
        expect(chip.textContent).toBe('Shift+Enter');
        expect(submit.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
        expect(submit.getByRole('button', { name: 'Clear shortcut' })).toBeTruthy();
    });

    it('captures a combo and assigns it to the submit binding only', async () => {
        mockUserAgent(WIN_UA);
        const { create, submit, onDirty } = renderTab();
        const chip = submit.getByRole('button', { name: /create task \(submit\) shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'Enter', shiftKey: true });
        expect(chip.textContent).toBe('Shift+Enter');
        // The other row is untouched.
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('C');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('resets to the platform default', async () => {
        mockUserAgent(WIN_UA);
        const { submit, onDirty } = renderTab({ createTaskSubmitKey: 'shift+enter' });
        await fireEvent.click(submit.getByRole('button', { name: 'Reset to default' }));
        expect(submit.getByRole('button', { name: /create task \(submit\) shortcut/i }).textContent).toBe('Ctrl+Enter');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('clears the submit binding (disables it)', async () => {
        mockUserAgent(WIN_UA);
        const { submit, onDirty } = renderTab();
        await fireEvent.click(submit.getByRole('button', { name: 'Clear shortcut' }));
        expect(submit.getByRole('button', { name: /create task \(submit\) shortcut/i }).textContent).toBe('None');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });
});

describe('PreferencesTab start-task row', () => {
    it('shows the platform default: Ctrl+Enter on non-Mac', () => {
        mockUserAgent(WIN_UA);
        const { start } = renderTab();
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('Ctrl+Enter');
    });

    it('shows the platform default: Cmd+Enter on Mac', () => {
        mockUserAgent(MAC_UA);
        const { start } = renderTab();
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('Cmd+Enter');
    });

    it('shows a custom binding formatted, with Reset and Clear controls', () => {
        mockUserAgent(WIN_UA);
        const { start } = renderTab({ startTaskKey: 'shift+enter' });
        const chip = start.getByRole('button', { name: /start task shortcut/i });
        expect(chip.textContent).toBe('Shift+Enter');
        expect(start.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
        expect(start.getByRole('button', { name: 'Clear shortcut' })).toBeTruthy();
    });

    it('shows "None" and no Clear control when the shortcut is disabled', () => {
        mockUserAgent(WIN_UA);
        const { start } = renderTab({ startTaskKey: '' });
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('None');
        expect(start.queryByRole('button', { name: 'Clear shortcut' })).toBeNull();
        expect(start.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
    });

    it('captures a combo and assigns it to the start-task binding only', async () => {
        mockUserAgent(WIN_UA);
        const { start, submit, onDirty } = renderTab();
        const chip = start.getByRole('button', { name: /start task shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'Enter', shiftKey: true });
        expect(chip.textContent).toBe('Shift+Enter');
        // The other rows are untouched.
        expect(submit.getByRole('button', { name: /create task \(submit\) shortcut/i }).textContent).toBe('Ctrl+Enter');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('resets to the platform default', async () => {
        mockUserAgent(WIN_UA);
        const { start, onDirty } = renderTab({ startTaskKey: 'shift+enter' });
        await fireEvent.click(start.getByRole('button', { name: 'Reset to default' }));
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('Ctrl+Enter');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('clears the shortcut (disables it)', async () => {
        mockUserAgent(WIN_UA);
        const { start, onDirty } = renderTab();
        await fireEvent.click(start.getByRole('button', { name: 'Clear shortcut' }));
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('None');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('describes its purpose for the task view', () => {
        mockUserAgent(WIN_UA);
        const { start } = renderTab();
        expect(start.getAllByText(/task view/i).length).toBeGreaterThan(0);
    });
});
