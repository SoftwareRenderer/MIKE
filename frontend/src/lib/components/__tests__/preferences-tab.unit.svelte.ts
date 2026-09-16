import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup, within, waitFor } from '@testing-library/svelte';
import PreferencesTab from '$lib/components/ConfigModal/PreferencesTab.svelte';

const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';
const WIN_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
});

function mockUserAgent(ua: string) {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(ua);
}

/** Render the tab with per-row scoped queries (rows share class/aria names). */
function renderTab(props: { createTaskKey?: string; createTaskSubmitKey?: string; openTerminalKey?: string; startTaskKey?: string; toggleFullscreenKey?: string; multiSelectClickKey?: string } = {}) {
    const onDirty = vi.fn();
    const utils = render(PreferencesTab, {
        props: { createTaskKey: undefined, createTaskSubmitKey: undefined, openTerminalKey: undefined, startTaskKey: undefined, toggleFullscreenKey: undefined, multiSelectClickKey: undefined, onDirty, ...props },
    });
    const row = (id: string) => within(utils.container.querySelector(`[data-shortcut="${id}"]`)!);
    return {
        ...utils,
        onDirty,
        create: row('create_task'),
        submit: row('create_task_submit'),
        terminal: row('open_terminal'),
        start: row('start_task'),
        fullscreen: row('toggle_fullscreen'),
        multiSelect: row('multi_select_click'),
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

describe('PreferencesTab hide-model-badge toggle', () => {
    const TOGGLE = 'button[aria-label="Hide model badge"]';

    function renderModelBadgeTab(props: { hideModelBadge?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(PreferencesTab, {
            props: { createTaskKey: undefined, createTaskSubmitKey: undefined, hideModelBadge: false, onDirty, ...props },
        });
        return { ...utils, onDirty, toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement };
    }

    it('renders the toggle in its own UI section, off by default', () => {
        const { getByText, toggle } = renderModelBadgeTab();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
        // The toggle lives in a dedicated UI section, not under Keyboard Shortcuts.
        const section = toggle()!.closest('.section');
        expect(section?.querySelector('.section-label')?.textContent).toBe('UI');
        expect(getByText('Hide model badge')).toBeTruthy();
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderModelBadgeTab({ hideModelBadge: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderModelBadgeTab();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });
});

describe('PreferencesTab auto-maximize-chat toggle', () => {
    const TOGGLE = 'button[aria-label="Auto maximize chat visibility on mobile"]';

    function renderAutoMaximizeTab(props: { autoMaximizeChat?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(PreferencesTab, {
            props: { createTaskKey: undefined, createTaskSubmitKey: undefined, onDirty, ...props },
        });
        return { ...utils, onDirty, toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement };
    }

    it('is on when nothing is stored for it', () => {
        const { toggle } = renderAutoMaximizeTab();
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('reflects a stored false', () => {
        const { toggle } = renderAutoMaximizeTab({ autoMaximizeChat: false });
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderAutoMaximizeTab();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });
});

/** jsdom has no Notification, and the row reads the permission at render time, so the stub goes in first. */
function stubNotification(permission: 'default' | 'granted' | 'denied', answer?: 'granted' | 'denied') {
    const requestPermission = vi.fn().mockResolvedValue(answer ?? (permission === 'default' ? 'granted' : permission));
    vi.stubGlobal('Notification', class {
        static permission = permission;
        static requestPermission = requestPermission;
    });
    return requestPermission;
}

describe('PreferencesTab notify-on-review toggle', () => {
    const TOGGLE = 'button[aria-label="Notify when a task enters Review"]';
    const DESC = '#notify-on-review-desc';

    function renderNotifyTab(props: { notifyOnReview?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(PreferencesTab, {
            props: { createTaskKey: undefined, createTaskSubmitKey: undefined, onDirty, ...props },
        });
        return {
            ...utils,
            onDirty,
            toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement,
            desc: () => utils.container.querySelector(DESC)?.textContent?.trim(),
        };
    }

    it('is off when nothing is stored for it, and lives in the UI section', () => {
        stubNotification('granted');
        const { getByText, toggle, desc } = renderNotifyTab();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
        expect(toggle()!.closest('.section')?.querySelector('.section-label')?.textContent).toBe('UI');
        expect(getByText('Notify when a task enters Review')).toBeTruthy();
        expect(desc()).toBe('Send a browser notification when a task starts waiting for you while you are looking elsewhere');
    });

    it('reflects a stored preference', () => {
        stubNotification('granted');
        expect(renderNotifyTab({ notifyOnReview: true }).toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        stubNotification('granted');
        const { toggle, onDirty } = renderNotifyTab();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('asks the browser once, on the click that enables it', async () => {
        const requestPermission = stubNotification('default');
        const { toggle, desc } = renderNotifyTab();
        await fireEvent.click(toggle()!);
        expect(requestPermission).toHaveBeenCalledTimes(1);
        await waitFor(() => expect(desc()).toBe('Send a browser notification when a task starts waiting for you while you are looking elsewhere'));
    });

    it('does not ask when the click disables it', async () => {
        const requestPermission = stubNotification('granted');
        const { toggle } = renderNotifyTab({ notifyOnReview: true });
        await fireEvent.click(toggle()!);
        expect(requestPermission).not.toHaveBeenCalled();
    });

    it('names what to do while the browser refuses, and only while enabled', async () => {
        stubNotification('denied');
        const { toggle, desc } = renderNotifyTab();
        expect(desc()).toBe('Send a browser notification when a task starts waiting for you while you are looking elsewhere');
        await fireEvent.click(toggle()!);
        expect(desc()).toBe('This browser is not showing notifications for this site — allow them in its site settings');
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('says the browser has no notifications rather than blaming the user', () => {
        // No Notification stubbed at all: a plain-http origin or a webview.
        const { desc, toggle } = renderNotifyTab({ notifyOnReview: true });
        expect(desc()).toBe('This browser has no notifications');
        expect(toggle()?.disabled).toBe(false);
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

    it('drops the rejection hint once a valid key is captured', async () => {
        const { create, queryByText } = renderTab();
        const chip = create.getByRole('button', { name: /create task shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'F5' });
        expect(queryByText(/invalid/i)).toBeTruthy();

        await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
        expect(chip.textContent).toBe('Ctrl+K');
        expect(queryByText(/invalid/i)).toBeNull();
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

describe('PreferencesTab toggle-fullscreen row', () => {
    it('shows the default key "Ctrl+Up" when no binding is stored (any platform)', () => {
        mockUserAgent(MAC_UA);
        const { fullscreen } = renderTab();
        expect(fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i }).textContent).toBe('Ctrl+Up');
        mockUserAgent(WIN_UA);
        const { fullscreen: fullscreenWin } = renderTab();
        expect(fullscreenWin.getByRole('button', { name: /toggle fullscreen shortcut/i }).textContent).toBe('Ctrl+Up');
    });

    it('shows a custom binding formatted, with Reset and Clear controls', () => {
        const { fullscreen } = renderTab({ toggleFullscreenKey: 'shift+alt+f' });
        const chip = fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i });
        expect(chip.textContent).toBe('Shift+Alt+F');
        expect(fullscreen.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
        expect(fullscreen.getByRole('button', { name: 'Clear shortcut' })).toBeTruthy();
    });

    it('shows "None" and no Clear control when the shortcut is disabled', () => {
        const { fullscreen } = renderTab({ toggleFullscreenKey: '' });
        expect(fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i }).textContent).toBe('None');
        expect(fullscreen.queryByRole('button', { name: 'Clear shortcut' })).toBeNull();
        expect(fullscreen.getByRole('button', { name: 'Reset to default' })).toBeTruthy();
    });

    it('captures a combo and assigns it to the toggle-fullscreen binding only', async () => {
        const { fullscreen, start, onDirty } = renderTab();
        const chip = fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i });
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'F', shiftKey: true, altKey: true });
        expect(chip.textContent).toBe('Shift+Alt+F');
        // The other rows are untouched.
        expect(start.getByRole('button', { name: /start task shortcut/i }).textContent).toBe('Ctrl+Enter');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('resets to the default binding', async () => {
        const { fullscreen, onDirty } = renderTab({ toggleFullscreenKey: 'shift+alt+f' });
        await fireEvent.click(fullscreen.getByRole('button', { name: 'Reset to default' }));
        expect(fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i }).textContent).toBe('Ctrl+Up');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('clears the shortcut (disables it)', async () => {
        const { fullscreen, onDirty } = renderTab();
        await fireEvent.click(fullscreen.getByRole('button', { name: 'Clear shortcut' }));
        expect(fullscreen.getByRole('button', { name: /toggle fullscreen shortcut/i }).textContent).toBe('None');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('describes its purpose for the focused area', () => {
        const { fullscreen } = renderTab();
        expect(fullscreen.getAllByText(/focused area/i).length).toBeGreaterThan(0);
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

describe('PreferencesTab board sort toggle', () => {
    const TOGGLE = 'button[aria-label="Oldest Task on top"]';

    function renderSortTab(props: { sortOldestFirst?: boolean } = {}) {
        const onDirty = vi.fn();
        const utils = render(PreferencesTab, {
            props: { createTaskKey: undefined, sortOldestFirst: false, onDirty, ...props },
        });
        return { ...utils, onDirty, toggle: () => utils.container.querySelector(TOGGLE) as HTMLButtonElement };
    }

    it('renders the toggle, off by default (newest move on top)', () => {
        const { toggle } = renderSortTab();
        expect(toggle()).toBeTruthy();
        expect(toggle()?.getAttribute('aria-checked')).toBe('false');
    });

    it('reflects a stored preference', () => {
        const { toggle } = renderSortTab({ sortOldestFirst: true });
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
    });

    it('toggles and reports dirty', async () => {
        const { toggle, onDirty } = renderSortTab();
        await fireEvent.click(toggle()!);
        expect(toggle()?.getAttribute('aria-checked')).toBe('true');
        expect(onDirty).toHaveBeenCalledTimes(1);
    });

    it('explains what turning it on does', () => {
        const { container } = renderSortTab();
        const toggle = container.querySelector(TOGGLE) as HTMLButtonElement;
        const desc = container.querySelector(`#${toggle.getAttribute('aria-describedby')!}`);
        expect(desc?.textContent).toMatch(/least recently moved/i);
    });
});

describe('PreferencesTab multi-select click row', () => {
    const chipOf = (row: ReturnType<typeof renderTab>['multiSelect']) =>
        row.getByRole('button', { name: /multi-select cards shortcut/i });

    it('shows the platform default: Cmd+Click on Mac, Ctrl+Click elsewhere', () => {
        mockUserAgent(MAC_UA);
        expect(chipOf(renderTab().multiSelect).textContent).toBe('Cmd+Click');
        cleanup();
        mockUserAgent(WIN_UA);
        expect(chipOf(renderTab().multiSelect).textContent).toBe('Ctrl+Click');
    });

    it('shows a stored binding formatted', () => {
        const { multiSelect } = renderTab({ multiSelectClickKey: 'shift+click' });
        expect(chipOf(multiSelect).textContent).toBe('Shift+Click');
    });

    it('captures a modifier+click and assigns it to this row only', async () => {
        const { multiSelect, create, onDirty } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        expect(chip.textContent).toBe('Now modifier+click...');
        await fireEvent.mouseDown(window, { ctrlKey: true });
        expect(chip.textContent).toBe('Ctrl+Click');
        expect(onDirty).toHaveBeenCalledTimes(1);
        // Keyboard rows are untouched by a mouse capture.
        expect(create.getByRole('button', { name: /create task shortcut/i }).textContent).toBe('C');
    });

    it('captures a Cmd+Shift+Click combo in canonical order', async () => {
        const { multiSelect } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.mouseDown(window, { shiftKey: true, metaKey: true });
        expect(chip.textContent).toBe('Cmd+Shift+Click');
    });

    it('rejects an unmodified click — a bare click stays the card\u2019s own action', async () => {
        const { multiSelect, getByText, onDirty } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.mouseDown(window, { button: 0 });
        expect(chip.textContent).toBe('Now modifier+click...');
        expect(getByText(/invalid combo/i)).toBeTruthy();
        expect(onDirty).not.toHaveBeenCalled();
    });

    it('drops the rejection hint once a valid click binding is captured', async () => {
        const { multiSelect, queryByText } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.mouseDown(window, { button: 0 });
        expect(queryByText(/invalid/i)).toBeTruthy();

        await fireEvent.mouseDown(window, { button: 0, ctrlKey: true, shiftKey: true });
        expect(chip.textContent).toBe('Ctrl+Shift+Click');
        expect(queryByText(/invalid/i)).toBeNull();
    });

    it('never shows the keyboard sentence for a rejected mouse click', async () => {
        const { multiSelect, queryByText } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.mouseDown(window, { button: 0 });
        expect(queryByText(/invalid combo/i)).toBeTruthy();
        expect(queryByText(/invalid key/i)).toBeNull();

        // The hint must not change wording when the capture it belongs to ends.
        await fireEvent.keyDown(window, { key: 'Escape' });
        expect(chip.textContent).not.toBe('Now modifier+click...');
        expect(queryByText(/invalid key/i)).toBeNull();
    });

    it('ignores non-primary buttons (middle click belongs to the browser)', async () => {
        const { multiSelect, onDirty } = renderTab();
        await fireEvent.click(chipOf(multiSelect));
        await fireEvent.mouseDown(window, { button: 1, ctrlKey: true });
        expect(onDirty).not.toHaveBeenCalled();
    });

    it('rejects keypresses while capturing a mouse combo', async () => {
        const { multiSelect, getByText, onDirty } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
        expect(chip.textContent).toBe('Now modifier+click...');
        expect(getByText(/invalid combo/i)).toBeTruthy();
        expect(onDirty).not.toHaveBeenCalled();
    });

    it('stays clickable: Escape and a second chip click cancel capture', async () => {
        const { multiSelect } = renderTab();
        const chip = chipOf(multiSelect);
        await fireEvent.click(chip);
        await fireEvent.keyDown(window, { key: 'Escape' });
        expect(chip.textContent).not.toBe('Now modifier+click...');
        await fireEvent.click(chip);
        expect(chip.textContent).toBe('Now modifier+click...');
        // Clicking the chip itself while capturing cancels instead of binding.
        await fireEvent.mouseDown(chip);
        await fireEvent.click(chip);
        expect(chip.textContent).not.toBe('Now modifier+click...');
    });

    it('clears (disables) and resets to the platform default', async () => {
        mockUserAgent(MAC_UA);
        const { multiSelect, onDirty } = renderTab({ multiSelectClickKey: 'shift+click' });
        await fireEvent.click(multiSelect.getByRole('button', { name: 'Clear shortcut' }));
        expect(chipOf(multiSelect).textContent).toBe('None');
        await fireEvent.click(multiSelect.getByRole('button', { name: 'Reset to default' }));
        expect(chipOf(multiSelect).textContent).toBe('Cmd+Click');
        expect(onDirty).toHaveBeenCalledTimes(2);
    });
});
