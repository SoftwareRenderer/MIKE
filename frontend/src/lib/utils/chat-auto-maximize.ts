/**
 * When the task chat maximizes itself, extracted from CardDetail.svelte for unit tests.
 *
 * Three layers agree here: ChatForm decides what counts as focus leaving the chat input,
 * CardDetail owns the maximize state the sticky toggle and shortcut also drive, and this
 * file decides which focus event moves that state and which of those moves blur may undo.
 */

/** What the preference and the layout allow. */
export interface ChatAutoMaximizeRules {
    /** "Auto maximize chat visibility on mobile"; unset means on. */
    enabled: boolean;
    /** The narrow single-column layout, where the chat has no room of its own. */
    isMobile: boolean;
    /** The floating task view floats over the board and keeps its own size. */
    floating: boolean;
}

export interface ChatMaximizeState {
    /** The chat column is maximized — the same state the sticky toggle and shortcut drive. */
    fullscreen: boolean;
    /** That maximize came from the input holding focus, so losing focus may undo it. */
    autoMaximized: boolean;
}

/** Collapse, and hand ownership back to the user. */
export function chatCollapseState(): ChatMaximizeState {
    return { fullscreen: false, autoMaximized: false };
}

/** Focus entered the chat input. */
export function chatFocusState(rules: ChatAutoMaximizeRules, state: ChatMaximizeState): ChatMaximizeState {
    if (!rules.enabled || !rules.isMobile || rules.floating) return state;
    // Already maximized: whoever maximized it keeps the right to minimize it.
    if (state.fullscreen) return state;
    return { fullscreen: true, autoMaximized: true };
}

/** Focus left the chat input — only the maximize this rule applied is reversed. */
export function chatBlurState(state: ChatMaximizeState): ChatMaximizeState {
    return state.autoMaximized ? chatCollapseState() : state;
}

/** The sticky toggle or its shortcut: the user owns the state, so no blur reverses it. */
export function chatManualToggleState(state: ChatMaximizeState): ChatMaximizeState {
    return { fullscreen: !state.fullscreen, autoMaximized: false };
}
