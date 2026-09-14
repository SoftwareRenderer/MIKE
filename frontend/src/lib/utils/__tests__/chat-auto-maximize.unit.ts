import { describe, it, expect } from 'vitest';
import {
    chatBlurState,
    chatCollapseState,
    chatFocusState,
    chatManualToggleState,
    chatSendState,
    type ChatAutoMaximizeRules,
    type ChatMaximizeState,
} from '$lib/utils/chat-auto-maximize';

// The maximize is the sticky toggle's own state, so the auto rule may only undo the
// transition it made itself — and it does not act at all off the mobile layout, in the
// floating window, or while the preference is off.

const COLLAPSED: ChatMaximizeState = { fullscreen: false, autoMaximized: false };
const AUTO_MAXIMIZED: ChatMaximizeState = { fullscreen: true, autoMaximized: true };
const USER_MAXIMIZED: ChatMaximizeState = { fullscreen: true, autoMaximized: false };

const rules: ChatAutoMaximizeRules = { enabled: true, isMobile: true, floating: false };

describe('chatFocusState', () => {
    it('maximizes on focus in the mobile task view', () => {
        expect(chatFocusState(rules, COLLAPSED)).toEqual(AUTO_MAXIMIZED);
    });

    it('does nothing while the preference is off', () => {
        expect(chatFocusState({ ...rules, enabled: false }, COLLAPSED)).toEqual(COLLAPSED);
    });

    it('does nothing on the desktop layout', () => {
        expect(chatFocusState({ ...rules, isMobile: false }, COLLAPSED)).toEqual(COLLAPSED);
    });

    it('does nothing in the floating task view, which shares the mobile layout', () => {
        expect(chatFocusState({ ...rules, floating: true }, COLLAPSED)).toEqual(COLLAPSED);
    });

    it('claims nothing when the panel is already maximized by hand', () => {
        expect(chatFocusState(rules, USER_MAXIMIZED)).toEqual(USER_MAXIMIZED);
    });

    it('keeps the claim it already had', () => {
        expect(chatFocusState(rules, AUTO_MAXIMIZED)).toEqual(AUTO_MAXIMIZED);
    });
});

describe('chatBlurState', () => {
    it('reverses the maximize the focus rule applied', () => {
        expect(chatBlurState(AUTO_MAXIMIZED)).toEqual(COLLAPSED);
    });

    it('leaves a manual maximize standing', () => {
        expect(chatBlurState(USER_MAXIMIZED)).toEqual(USER_MAXIMIZED);
    });

    it('leaves a collapsed panel collapsed', () => {
        expect(chatBlurState(COLLAPSED)).toEqual(COLLAPSED);
    });
});

describe('chatSendState', () => {
    it('reverses the maximize the focus rule applied', () => {
        expect(chatSendState(AUTO_MAXIMIZED)).toEqual(COLLAPSED);
    });

    it('leaves a manual maximize standing', () => {
        expect(chatSendState(USER_MAXIMIZED)).toEqual(USER_MAXIMIZED);
    });

    it('leaves a collapsed panel collapsed', () => {
        expect(chatSendState(COLLAPSED)).toEqual(COLLAPSED);
    });
});

describe('chatManualToggleState', () => {
    it('collapses an auto maximize and drops the claim, so blur cannot act twice', () => {
        expect(chatManualToggleState(AUTO_MAXIMIZED)).toEqual(COLLAPSED);
    });

    it('maximizes from collapsed and owns the new state', () => {
        expect(chatManualToggleState(COLLAPSED)).toEqual(USER_MAXIMIZED);
    });
});

describe('chatCollapseState', () => {
    it('hands ownership back to the user', () => {
        expect(chatCollapseState()).toEqual(COLLAPSED);
    });
});
