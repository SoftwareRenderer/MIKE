/**
 * LSP hover card with a clickable "Go to Definition" action. Not a `hoverTooltip()` source:
 * CodeMirror dismisses a hover the moment the pointer leaves the position, which is the motion
 * needed to press the button, so open and close are ours. `hoverTime` of stillness opens the card;
 * the pointer on the identifier or inside the card keeps it; `graceMs` after the pointer leaves
 * both, a scroll from outside, or any doc change closes it.
 */

import { EditorView, ViewPlugin, showTooltip, type PluginValue, type Tooltip, type TooltipView, type ViewUpdate } from '@codemirror/view';
import { t } from '$lib/i18n';
import { StateEffect, StateField, type Extension, type Text } from '@codemirror/state';
import { sanitizeHTML } from '$lib/utils/sanitize';
import { LSPPlugin } from '@codemirror/lsp-client';
import { inBox } from './hit-box';
import { identifierRange, pickDefinitionTarget, toLspPosition, type CharRange, type DefinitionTarget, type LspPosition } from './lsp-definition';

// ─── Pure state machine ──────────────────────────────────────────────────────

/**
 * Where the pointer is relative to the card. `'other-token'` is an identifier other than the
 * card's, which a geometry check cannot tell from a pointer heading for the card.
 *
 * @knipignore The transition tests feed one input per zone.
 */
export type HoverZone = 'token' | 'tooltip' | 'other-token' | 'outside';

type HoverPhase = 'idle' | 'arming' | 'open';

/** @knipignore The transition tests assert the state after each input. */
export interface HoverMachineState {
    readonly phase: HoverPhase;
    /** Identifier the card is (or will be) about. */
    readonly token: CharRange | null;
    /** True while the close-grace timer is running. */
    readonly gracePending: boolean;
}

/** @knipignore The state every transition sequence starts from. */
export const HOVER_IDLE: HoverMachineState = { phase: 'idle', token: null, gracePending: false };

/** @knipignore The transition tests build these; the plugin builds them from DOM events. */
export type HoverInput =
    | { type: 'pointermove'; zone: HoverZone; token: CharRange | null }
    | { type: 'hover-elapsed' }
    | { type: 'grace-elapsed' }
    | { type: 'scroll'; zone: HoverZone }
    | { type: 'doc-change' }
    /** The card turned out to have nothing to show, or the connection went away. */
    | { type: 'dismiss' };

/** @knipignore The transition tests assert the effects rather than the timers they stand for. */
export type HoverEffect =
    | 'arm'
    | 'cancel-arm'
    | 'open'
    | 'close'
    | 'grace'
    | 'cancel-grace';

export interface HoverDecision {
    state: HoverMachineState;
    effects: HoverEffect[];
}

/** @knipignore The transition tests assert a pointer move to another identifier by range. */
export function sameToken(a: CharRange | null, b: CharRange | null): boolean {
    return !!a && !!b && a.from === b.from && a.to === b.to;
}

function keep(state: HoverMachineState, effects: HoverEffect[] = []): HoverDecision {
    return { state, effects };
}

/** Pointer is where the card wants it: drop any pending close. */
function settle(state: HoverMachineState): HoverDecision {
    return state.gracePending
        ? { state: { ...state, gracePending: false }, effects: ['cancel-grace'] }
        : keep(state);
}

/**
 * Start the dwell for `token`. A card already on screen is about a symbol the pointer left, so it
 * closes rather than being re-targeted.
 */
function rearm(state: HoverMachineState, token: CharRange | null): HoverDecision {
    if (sameToken(state.token, token) && state.phase === 'arming') return settle(state);
    // No grace to cancel unless a card is up: `gracePending` is only ever set on an open state.
    if (state.phase !== 'open') return { state: { phase: 'arming', token, gracePending: false }, effects: ['arm'] };
    return { state: { phase: 'arming', token, gracePending: false }, effects: ['close', 'cancel-grace', 'arm'] };
}

function shutdown(state: HoverMachineState): HoverDecision {
    if (state.phase === 'idle') return keep(state);
    return { state: HOVER_IDLE, effects: ['close', 'cancel-arm', 'cancel-grace'] };
}

/**
 * Next state plus the timer/UI side effects the caller must carry out. `token` on a `pointermove`
 * is the identifier under the pointer, null when there is none.
 *
 * @knipignore The plugin feeds it only DOM events; the transitions are tested here.
 */
export function reduceHover(state: HoverMachineState, input: HoverInput): HoverDecision {
    switch (input.type) {
        case 'pointermove': {
            const { zone, token } = input;
            if (zone === 'tooltip') {
                // Only meaningful while open; elsewhere it means the pointer left the identifier.
                if (state.phase === 'open') return settle(state);
                return state.phase === 'idle' ? keep(state) : shutdown(state);
            }
            if (zone === 'outside') {
                if (state.phase === 'open') {
                    return state.gracePending
                        ? keep(state)
                        : { state: { ...state, gracePending: true }, effects: ['grace'] };
                }
                return state.phase === 'idle' ? keep(state) : shutdown(state);
            }
            // 'token' | 'other-token' — over an identifier.
            if (state.phase === 'idle') return rearm(HOVER_IDLE, token);
            if (state.phase === 'arming') return rearm(state, token);
            // Another identifier is a different question, and this one was already left once.
            // Dwell again.
            if (!sameToken(state.token, token)) return rearm(state, token);
            return state.gracePending ? rearm(state, token) : settle(state);
        }
        case 'hover-elapsed':
            if (state.phase !== 'arming' || !state.token) return keep(state);
            return { state: { phase: 'open', token: state.token, gracePending: false }, effects: ['open'] };
        case 'grace-elapsed':
            if (state.phase !== 'open' || !state.gracePending) return keep(state);
            return { state: HOVER_IDLE, effects: ['close'] };
        case 'scroll':
            // A scroll that starts inside the card is the user reading it.
            if (input.zone === 'tooltip' && state.phase === 'open') return settle(state);
            return shutdown(state);
        case 'doc-change':
        case 'dismiss':
            return shutdown(state);
    }
}

// The pointer-vs-card hit test lives in `hit-box.ts`, shared with `closeHoverOnScroll` so both
// agree on when the tooltip is being pointed at.

// ─── Card ────────────────────────────────────────────────────────────────────

/** Minimal structural view of an LSP `Hover` payload (no protocol dependency). */
interface RawMarkup {
    contents?: unknown;
}

type MarkedString = string | { language?: string; value?: string; kind?: string };

function fenced(language: string, value: string): string {
    const fence = value.includes('```') ? '````' : '```';
    return `${fence}${language}\n${value}\n${fence}`;
}

function renderMarked(plugin: LSPPlugin, item: MarkedString): string {
    if (typeof item === 'string') return plugin.docToHTML(item, 'markdown');
    if (!item || typeof item !== 'object') return '';
    // MarkupContent carries its own kind: plaintext must stay escaped, not be run through
    // markdown.
    if ('kind' in item && item.kind) return plugin.docToHTML(item as { kind: 'markdown' | 'plaintext'; value: string });
    if ('language' in item && item.language) {
        return plugin.docToHTML({ kind: 'markdown', value: fenced(item.language, item.value ?? '') }, 'markdown');
    }
    if ('value' in item) return plugin.docToHTML(String(item.value ?? ''), 'markdown');
    return '';
}

/**
 * A language server separates doc sections with a rule the card's own layout provides; the
 * sanitizer refuses everything else by default.
 */
const HOVER_FORBID_TAGS = ['hr'] as const;

/**
 * `Hover.contents` (string | MarkupContent | MarkedString[]) to sanitized HTML.
 *
 * @knipignore The escaping and the `hr` refusal are asserted on the returned markup.
 */
export function hoverDocsHTML(plugin: LSPPlugin, hover: RawMarkup | null | undefined): string {
    if (!hover || hover.contents == null) return '';
    const contents = hover.contents;
    let html: string;
    try {
        html = Array.isArray(contents)
            ? contents.map(item => renderMarked(plugin, item as MarkedString)).join('<br>')
            : renderMarked(plugin, contents as MarkedString);
    } catch {
        return '';
    }

    return sanitizeHTML(html, HOVER_FORBID_TAGS);
}

/**
 * The card's DOM. The action row is the bottom section: with `above: true` the card keeps its
 * bottom edge on the code line, so late documentation grows upward and the button does not move.
 *
 * @knipignore The status line and the button's position are read off this DOM.
 */
export class HoverCard {
    readonly dom = document.createElement('div');
    private readonly status: HTMLElement;
    private readonly docs: HTMLElement;
    private readonly row: HTMLElement;
    private readonly button: HTMLButtonElement;
    private destroyed = false;

    constructor(onJump: (target: DefinitionTarget) => void) {
        this.dom.className = 'cm-lsp-action-card';
        const body = document.createElement('div');
        body.className = 'cm-lsp-action-card-body';
        this.status = document.createElement('div');
        this.status.className = 'cm-lsp-action-status';
        this.status.textContent = '…';
        this.docs = document.createElement('div');
        this.docs.className = 'cm-lsp-action-docs cm-lsp-documentation';
        this.docs.hidden = true;
        body.append(this.status, this.docs);

        this.row = document.createElement('div');
        this.row.className = 'cm-lsp-action-row';
        this.row.hidden = true;
        this.button = document.createElement('button');
        this.button.type = 'button';
        this.button.className = 'cm-lsp-action-button';
        this.button.textContent = t('lsp.goToDefinition');
        this.button.addEventListener('click', e => {
            e.preventDefault();
            e.stopPropagation();
            const target = this.target;
            if (target) onJump(target);
        });
        // The card overlaps the code; without this CodeMirror would move the caret under the
        // button.
        this.dom.addEventListener('mousedown', e => {
            e.preventDefault();
            e.stopPropagation();
        });
        this.row.append(this.button);
        this.dom.append(body, this.row);
    }

    private target: DefinitionTarget | null = null;

    get isDestroyed(): boolean {
        return this.destroyed;
    }

    showDocs(html: string): void {
        if (this.destroyed) return;
        this.status.hidden = true;
        if (!html) return;
        this.docs.innerHTML = html;
        this.docs.hidden = false;
    }

    showAction(target: DefinitionTarget): void {
        if (this.destroyed) return;
        this.target = target;
        this.row.hidden = false;
    }

    /** The wait has a name. */
    showWaiting(text: string): void {
        if (this.destroyed) return;
        this.status.textContent = text;
        this.status.hidden = false;
    }

    /** The wait ended without an answer, in a sentence that outlives the placeholder. */
    showConclusion(text: string): void {
        this.showWaiting(text);
    }

    /** Nothing to say — collapse the status line. */
    clearStatus(): void {
        if (this.destroyed) return;
        this.status.hidden = true;
    }

    destroy(): void {
        this.destroyed = true;
    }

    asTooltipView(): TooltipView {
        return {
            dom: this.dom,
            destroy: () => {
                this.destroy();
            },
        };
    }
}

// ─── State ───────────────────────────────────────────────────────────────────

const setHoverCard = StateEffect.define<Tooltip | null>();

const hoverCardField = StateField.define<Tooltip | null>({
    create: () => null,
    update(value, tr) {
        for (const e of tr.effects) if (e.is(setHoverCard)) value = e.value;
        // A card never outlives the text it was computed from.
        if (value && tr.docChanged) value = null;
        return value;
    },
    provide: f => showTooltip.from(f),
});

export interface LspHoverActionOptions {
    /** Stillness, in ms, over an identifier before the card opens. Default {@link DEFAULT_HOVER_TIME}. */
    hoverTime?: number;
    /** How long the card survives after the pointer leaves it. Default {@link DEFAULT_HOVER_GRACE}. */
    graceMs?: number;
    /** How long the bare placeholder is allowed to stand before it names the server. Default {@link DEFAULT_HOVER_WAITING}. */
    waitingMs?: number;
    /** How long one request may take before the card says it will not be answered. Default {@link DEFAULT_HOVER_CEILING}. */
    ceilingMs?: number;
    /** What to call the server this card is waiting for. */
    serverLabel?: () => string;
    /** Whether a definition target can be jumped to from this editor. */
    canJump?: (target: DefinitionTarget) => boolean;
    /** Perform the jump. */
    onJump: (target: DefinitionTarget) => void;
}

/**
 * How long the pointer has to rest on an identifier before anything appears.
 *
 * @knipignore This default and its fallback are asserted in the hover tests.
 */
export const DEFAULT_HOVER_TIME = 500;

/**
 * How long the card survives after the pointer leaves it and the identifier.
 *
 * @knipignore This default and its fallback are asserted in the hover tests.
 */
export const DEFAULT_HOVER_GRACE = 500;

/**
 * How long the placeholder waits alone before it says which server it is waiting for. It must speak
 * before the round trip's ceiling.
 *
 * @knipignore That it stays under the ceiling is asserted here, where both are visible.
 */
export const DEFAULT_HOVER_WAITING = 700;

/**
 * How long one hover/definition round trip may take before the card stops waiting on it. Under the
 * library's request timer and the bridge's, over the seconds a cold index needs.
 *
 * @knipignore That ordering is asserted here; only this file knows both ends.
 */
export const DEFAULT_HOVER_CEILING = 8000;

/** @knipignore The plugin reads one resolved record; the fallback per timing is asserted here. */
export function resolveHoverTimings(
    options: Pick<LspHoverActionOptions, 'hoverTime' | 'graceMs' | 'waitingMs' | 'ceilingMs'>,
): { hoverTime: number; graceMs: number; waitingMs: number; ceilingMs: number } {
    return {
        hoverTime: options.hoverTime ?? DEFAULT_HOVER_TIME,
        graceMs: options.graceMs ?? DEFAULT_HOVER_GRACE,
        waitingMs: options.waitingMs ?? DEFAULT_HOVER_WAITING,
        ceilingMs: options.ceilingMs ?? DEFAULT_HOVER_CEILING,
    };
}

/** The half of a card one of the wait clocks writes. */
export interface WaitStatus {
    showWaiting(text: string): void;
    showConclusion(text: string): void;
}

/**
 * Starts the two clocks a waiting card runs and returns the function that stops both. `live`
 * rejects a clock that fires for a card the pointer has left; the sentences are read when the
 * clock fires, so a server that introduces itself mid-hover is named by the name it gave.
 *
 * @knipignore The two clocks and their cancellation run under fake timers.
 */
export function armWaitClocks(
    card: WaitStatus,
    timings: { waitingMs: number; ceilingMs: number },
    sentences: { waiting: () => string; noAnswer: () => string },
    live: () => boolean,
): () => void {
    const waiting = setTimeout(() => {
        if (live()) card.showWaiting(sentences.waiting());
    }, timings.waitingMs);
    const ceiling = setTimeout(() => {
        if (live()) card.showConclusion(sentences.noAnswer());
    }, timings.ceilingMs);
    return () => {
        clearTimeout(waiting);
        clearTimeout(ceiling);
    };
}

/** `TextDocumentPositionParams`, shaped locally to avoid a protocol dependency. */
interface HoverRequestParams {
    textDocument: { uri: string };
    position: LspPosition;
}

const CACHE_LIMIT = 200;

/**
 * Per-view request memos, shared by the hover card and the click path. Keys are document offsets,
 * so any edit clears them.
 */
interface ViewCaches {
    docs: Map<string, Promise<string>>;
    definition: Map<string, Promise<DefinitionTarget | null>>;
}

const cachesByView = new WeakMap<EditorView, ViewCaches>();

function cachesFor(view: EditorView): ViewCaches {
    let caches = cachesByView.get(view);
    if (!caches) {
        caches = { docs: new Map(), definition: new Map() };
        cachesByView.set(view, caches);
    }
    return caches;
}

function clearCaches(view: EditorView): void {
    const caches = cachesByView.get(view);
    if (!caches) return;
    caches.docs.clear();
    caches.definition.clear();
}

/**
 * The per-token memo. A request that failed is not remembered as "no answer": one poisoned entry
 * otherwise strands that identifier for the life of the editor.
 *
 * @knipignore Retry-after-failure and eviction are asserted on a bare Map.
 */
export function remember<T>(cache: Map<string, Promise<T>>, key: string, make: () => Promise<T>): Promise<T> {
    const existing = cache.get(key);
    if (existing) return existing;
    const promise = make();
    cache.set(key, promise);
    if (cache.size > CACHE_LIMIT) {
        const oldest = cache.keys().next();
        if (!oldest.done) cache.delete(oldest.value);
    }
    // A failed request stays retryable: drop it instead of remembering "nothing".
    promise.catch(() => {
        if (cache.get(key) === promise) cache.delete(key);
    });
    return promise;
}

function tokenKey(plugin: LSPPlugin, token: CharRange): string {
    return `${plugin.uri}\u0000${token.from}:${token.to}`;
}

/**
 * Capability probe. `hasCapability` is missing from @codemirror/lsp-client 6.2.5's declarations,
 * so read the public record: a capability that has not arrived must not block a request, a known
 * absence must.
 */
function supported(plugin: LSPPlugin, cap: 'hoverProvider' | 'definitionProvider'): boolean {
    const caps = plugin.client.serverCapabilities;
    return caps ? !!caps[cap] : true;
}

/** Hover documentation, HTML-escaped/sanitized. */
function fetchDocs(plugin: LSPPlugin, params: HoverRequestParams): Promise<string> {
    if (!supported(plugin, 'hoverProvider')) return Promise.resolve('');
    // Flush unsaved edits first: the server must answer about this buffer.
    plugin.client.sync();
    return plugin.client
        .request<HoverRequestParams, RawMarkup | null>('textDocument/hover', params)
        .then(hover => hoverDocsHTML(plugin, hover));
}

function fetchDefinition(plugin: LSPPlugin, params: HoverRequestParams): Promise<DefinitionTarget | null> {
    if (!supported(plugin, 'definitionProvider')) return Promise.resolve(null);
    plugin.client.sync();
    return plugin.client
        .request<HoverRequestParams, unknown>('textDocument/definition', params)
        .then(result => pickDefinitionTarget(result));
}

/**
 * One request's params. Hover and definition need separate objects: `cancelRequest` picks the
 * request to cancel by the identity of the params it was sent with.
 */
function requestParams(plugin: LSPPlugin, doc: Text, token: CharRange): HoverRequestParams {
    return { textDocument: { uri: plugin.uri }, position: toLspPosition(doc, token.from) };
}

// ─── View plugin ─────────────────────────────────────────────────────────────

/** Options with the two timings resolved to their defaults. */
type ResolvedHoverOptions = LspHoverActionOptions & ReturnType<typeof resolveHoverTimings>;

class LspHoverValue implements PluginValue {
    private machine: HoverMachineState = HOVER_IDLE;
    private armTimer: ReturnType<typeof setTimeout> | null = null;
    private graceTimer: ReturnType<typeof setTimeout> | null = null;
    private card: HoverCard | null = null;
    /** Guards responses against the card/token they were asked for. */
    private seq = 0;
    /** Requests the current card is still waiting on, so abandoning it can tell the server. */
    private waits: { client: LSPPlugin['client']; params: HoverRequestParams }[] = [];
    /** Stops the two clocks the card on screen runs, if it is still waiting. */
    private stopWaits: (() => void) | null = null;
    private lastPlugin: LSPPlugin | null = null;
    /** Last pointer position, used to classify a scroll. */
    private pointer: { x: number; y: number } | null = null;
    /**
     * `EditorView.update` throws if a plugin dispatches while it runs, so a close asked for inside
     * `update` is handed to the next task.
     */
    private inUpdate = false;
    private destroyed = false;

    constructor(
        private readonly view: EditorView,
        private readonly opts: ResolvedHoverOptions,
    ) {
        view.dom.addEventListener('mousemove', this.onMouseMove);
        view.dom.addEventListener('mouseleave', this.onMouseLeave);
        view.scrollDOM.addEventListener('scroll', this.onScroll, { passive: true });
    }

    update(update: ViewUpdate): void {
        this.inUpdate = true;
        try {
            if (update.docChanged) {
                // Cache keys are document offsets, so an edit makes every one stale.
                clearCaches(update.view);
                this.feed({ type: 'doc-change' });
            }
            const plugin = LSPPlugin.get(update.view);
            if (plugin !== this.lastPlugin) {
                this.lastPlugin = plugin ?? null;
                clearCaches(update.view);
                this.feed({ type: 'dismiss' });
            }
        } finally {
            this.inUpdate = false;
        }
    }

    destroy(): void {
        this.destroyed = true;
        this.clearTimers();
        this.cancelWaits();
        this.view.dom.removeEventListener('mousemove', this.onMouseMove);
        this.view.dom.removeEventListener('mouseleave', this.onMouseLeave);
        this.view.scrollDOM.removeEventListener('scroll', this.onScroll);
        this.card?.destroy();
        this.card = null;
    }

    // ---- input ----

    private readonly onMouseMove = (event: MouseEvent): void => {
        this.pointer = { x: event.clientX, y: event.clientY };
        const { zone, token } = this.zoneAt(event.clientX, event.clientY);
        this.feed({ type: 'pointermove', zone, token });
    };

    private readonly onMouseLeave = (event: MouseEvent): void => {
        // The card lives inside view.dom, so only a truly departed pointer reaches this branch.
        if (this.card && !this.card.isDestroyed && this.card.dom.contains(event.relatedTarget as Node | null)) return;
        this.pointer = null;
        this.feed({ type: 'pointermove', zone: 'outside', token: null });
    };

    private readonly onScroll = (): void => {
        const inCard =
            !!this.pointer && !!this.card && !this.card.isDestroyed &&
            inBox(this.card.dom.getBoundingClientRect(), this.pointer.x, this.pointer.y);
        this.feed({ type: 'scroll', zone: inCard ? 'tooltip' : 'outside' });
    };

    /** Classify a pointer position against the card and the identifier. */
    private zoneAt(x: number, y: number): { zone: HoverZone; token: CharRange | null } {
        const card = this.card;
        if (card && !card.isDestroyed && inBox(card.dom.getBoundingClientRect(), x, y)) {
            return { zone: 'tooltip', token: this.machine.token };
        }
        const pos = this.view.posAtCoords({ x, y }, false);
        const token = pos == null ? null : identifierRange(this.view.state.doc, pos);
        if (!token) return { zone: 'outside', token: null };
        return { zone: sameToken(token, this.machine.token) ? 'token' : 'other-token', token };
    }

    // ---- machine ----

    private feed(input: HoverInput): void {
        const decision = reduceHover(this.machine, input);
        this.machine = decision.state;
        for (const effect of decision.effects) this.run(effect);
    }

    private run(effect: HoverEffect): void {
        switch (effect) {
            case 'arm':
                this.clearTimer('arm');
                this.armTimer = setTimeout(() => this.feed({ type: 'hover-elapsed' }), this.opts.hoverTime);
                return;
            case 'cancel-arm':
                this.clearTimer('arm');
                return;
            case 'grace':
                this.clearTimer('grace');
                this.graceTimer = setTimeout(() => this.feed({ type: 'grace-elapsed' }), this.opts.graceMs);
                return;
            case 'cancel-grace':
                this.clearTimer('grace');
                return;
            case 'open':
                if (this.machine.token) this.open(this.machine.token);
                return;
            case 'close':
                this.closeCard();
                return;
        }
    }

    private clearTimer(which: 'arm' | 'grace'): void {
        const timer = which === 'arm' ? this.armTimer : this.graceTimer;
        if (timer !== null) clearTimeout(timer);
        if (which === 'arm') this.armTimer = null;
        else this.graceTimer = null;
    }

    private clearTimers(): void {
        this.clearTimer('arm');
        this.clearTimer('grace');
        this.stopWaitClocks();
    }

    /** The clocks for the card being opened now; the card before it had its own stopped already. */
    private startWaitClocks(card: HoverCard, seq: number): void {
        this.stopWaitClocks();
        this.stopWaits = armWaitClocks(
            card,
            this.opts,
            { waiting: () => t('lsp.hoverWaiting', { server: this.serverLabel() }), noAnswer: this.noAnswerText },
            () => this.live(card, seq),
        );
    }

    private stopWaitClocks(): void {
        this.stopWaits?.();
        this.stopWaits = null;
    }

    /** The name the server introduced itself by. */
    private serverLabel(): string {
        const label = this.opts.serverLabel?.() ?? '';
        return label.trim() === '' ? t('lsp.hoverServerUnknown') : label;
    }

    private noAnswerText = (): string => t('lsp.hoverNoAnswer', { server: this.serverLabel() });

    /**
     * Tell the server to stop computing answers this card will never show. A server that ignores the
     * notification loses nothing.
     */
    private cancelWaits(): void {
        const waiting = this.waits;
        this.waits = [];
        for (const { client, params } of waiting) {
            try {
                client.cancelRequest(params);
            } catch {
            }
        }
    }

    // ---- card ----

    private open(token: CharRange): void {
        const plugin = LSPPlugin.get(this.view);
        if (!plugin) {
            this.feed({ type: 'dismiss' });
            return;
        }
        // Anything on screen when a dwell elapses is destroyed here, not left as a second tooltip.
        this.card?.destroy();
        this.cancelWaits();
        this.stopWaitClocks();
        const card = new HoverCard(target => this.opts.onJump(target));
        this.card = card;
        this.seq++;
        const seq = this.seq;
        const tooltip: Tooltip = {
            pos: token.from,
            end: token.to,
            above: true,
            create: () => card.asTooltipView(),
        };
        this.view.dispatch({ effects: setHoverCard.of(tooltip) });
        this.startWaitClocks(card, seq);
        this.fill(plugin, token, card, seq);
    }

    /** Whether this card is still the one on screen for the current dwell. */
    private live(card: HoverCard, seq: number): boolean {
        return !this.destroyed && !card.isDestroyed && seq === this.seq && this.machine.phase === 'open';
    }

    private closeCard(): void {
        this.card?.destroy();
        this.card = null;
        this.stopWaitClocks();
        this.cancelWaits();
        // A doc change may already have dropped the card.
        if (!this.view.state.field(hoverCardField)) return;
        if (this.inUpdate) {
            setTimeout(() => this.removeCard(), 0);
        } else {
            this.removeCard();
        }
    }

    private removeCard(): void {
        // A doc change may have dropped the card, and a deferred close may find the view gone.
        if (this.destroyed || !this.view.state.field(hoverCardField)) return;
        this.view.dispatch({ effects: setHoverCard.of(null) });
    }

    /**
     * Asks about one identifier and fills the card as the answers come. Each half paints on its own
     * clock: documentation must not wait on a definition still being computed.
     */
    private fill(plugin: LSPPlugin, token: CharRange, card: HoverCard, seq: number): void {
        const state = { docs: false, definition: false, failed: false, shown: false };
        const finish = (): void => {
            if (!this.live(card, seq)) return;
            this.stopWaitClocks();
            if (state.shown) {
                card.clearStatus();
                return;
            }
            // One half is still out there and may still have something to say.
            if (!state.docs || !state.definition) return;
            if (state.failed) card.showConclusion(this.noAnswerText());
            // Nothing to say — do not park an empty box on it.
            else this.feed({ type: 'dismiss' });
        };
        this.requestDocs(plugin, token).then(
            html => {
                state.docs = true;
                if (html && this.live(card, seq)) {
                    state.shown = true;
                    card.showDocs(html);
                }
                finish();
            },
            () => {
                state.docs = true;
                state.failed = true;
                finish();
            },
        );
        this.requestDefinition(plugin, token).then(
            target => {
                state.definition = true;
                const jumpable = !!target && (this.opts.canJump ? this.opts.canJump(target) : true);
                if (jumpable && target && this.live(card, seq)) {
                    state.shown = true;
                    card.showAction(target);
                }
                finish();
            },
            () => {
                state.definition = true;
                state.failed = true;
                finish();
            },
        );
    }

    /** Per-token memo, so a repeat hover over the same symbol is free and the machine never refires. */
    private requestDocs(plugin: LSPPlugin, token: CharRange): Promise<string> {
        const caches = cachesFor(this.view);
        const params = requestParams(plugin, this.view.state.doc, token);
        return remember(caches.docs, tokenKey(plugin, token), () =>
            this.track(plugin.client, params, () => fetchDocs(plugin, params)),
        );
    }

    private requestDefinition(plugin: LSPPlugin, token: CharRange): Promise<DefinitionTarget | null> {
        const caches = cachesFor(this.view);
        const params = requestParams(plugin, this.view.state.doc, token);
        return remember(caches.definition, tokenKey(plugin, token), () =>
            this.track(plugin.client, params, () => fetchDefinition(plugin, params)),
        );
    }

    /**
     * Runs one request while the card waits on it, so abandoning the card can name it. A request
     * leaves the list as soon as it settles.
     */
    private track<T>(client: LSPPlugin['client'], params: HoverRequestParams, make: () => Promise<T>): Promise<T> {
        const wait = { client, params };
        this.waits.push(wait);
        return make().finally(() => {
            const at = this.waits.indexOf(wait);
            if (at >= 0) this.waits.splice(at, 1);
        });
    }
}

// ─── Presentation ────────────────────────────────────────────────────────────

const hoverCardTheme = EditorView.baseTheme({
    '& .cm-lsp-action-card': {
        display: 'flex',
        flexDirection: 'column',
        maxWidth: '420px',
        padding: '4px 6px',
        fontSize: '12px',
        lineHeight: '1.45',
        pointerEvents: 'auto',
    },
    '& .cm-tooltip.cm-lsp-action-card': {
        maxHeight: '240px',
        overflow: 'hidden',
    },
    '& .cm-lsp-action-card-body': {
        minWidth: '0',
        minHeight: '0',
        overflowY: 'auto',
        overscrollBehavior: 'contain',
    },
    '& .cm-lsp-action-status': { color: 'var(--color-text-muted, #888)', padding: '1px 2px' },
    // Not `white-space: pre-wrap`: the renderer writes a newline between every tag, which pre-wrap
    // turns into a blank line between the signature and its prose.
    '& .cm-lsp-action-docs': { overflowWrap: 'anywhere' },
    '& .cm-lsp-action-docs pre': { margin: '2px 0', whiteSpace: 'pre-wrap' },
    '& .cm-lsp-action-row': { marginTop: '4px', flexShrink: 0 },
    '& .cm-lsp-action-button': {
        background: 'var(--color-accent, #4b6bfb)',
        color: '#fff',
        border: 'none',
        borderRadius: '3px',
        padding: '2px 8px',
        fontSize: '11px',
        fontWeight: 600,
        cursor: 'pointer',
    },
    '& .cm-lsp-action-button:hover': { opacity: 0.85 },
});

// ─── Extension ───────────────────────────────────────────────────────────────

/**
 * Hover card for the file's language server. It finds the connection through `LSPPlugin`, so it
 * survives a reconnect.
 */
function lspHoverActions(options: LspHoverActionOptions): Extension {
    const resolved: ResolvedHoverOptions = {
        ...options,
        ...resolveHoverTimings(options),
    };
    return [hoverCardField, ViewPlugin.define(view => new LspHoverValue(view, resolved)), hoverCardTheme];
}

// ─── Click path ──────────────────────────────────────────────────────────────

/**
 * Definition of the identifier at `pos`, sharing the hover card's cache. Null when there is no
 * identifier, no connection, or nothing to say — a jump is never attempted on a guess.
 */
function definitionAt(view: EditorView, pos: number): Promise<DefinitionTarget | null> {
    const plugin = LSPPlugin.get(view);
    if (!plugin) return Promise.resolve(null);
    const token = identifierRange(view.state.doc, pos);
    if (!token) return Promise.resolve(null);
    if (!supported(plugin, 'definitionProvider')) return Promise.resolve(null);
    const doc = view.state.doc;
    const params = requestParams(plugin, doc, token);
    return remember(cachesFor(view).definition, tokenKey(plugin, token), () =>
        fetchDefinition(plugin, params),
    ).catch(() => null);
}

/**
 * Modifier combination that turns a click into a jump (VS Code's gesture).
 *
 * @knipignore Each modifier is asserted on this predicate; a test click reaches no handler.
 */
export function isJumpModifier(event: { ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }): boolean {
    // Shift/Alt are taken by selection and multi-cursor semantics.
    return (event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey;
}

/**
 * Ctrl/Cmd+Click jumps to the definition of the identifier under the pointer. The click is left
 * uncancelled, so the caret still lands where it was clicked.
 */
function lspDefinitionClick(options: LspHoverActionOptions): Extension {
    return [
        EditorView.domEventHandlers({
            mousedown(event, view) {
                if (event.button !== 0 || !isJumpModifier(event)) return false;
                // A press on the hover card belongs to the card, not the text under it.
                if ((event.target as HTMLElement | null)?.closest?.('.cm-lsp-action-card')) return false;
                const pos = view.posAtCoords({ x: event.clientX, y: event.clientY }, false);
                if (pos == null) return false;
                // No `canJump` gate here: the jump explains why a target outside this workspace
                // cannot be opened, where a silent no-op explains nothing.
                void definitionAt(view, pos).then(target => {
                    if (target) options.onJump(target);
                });
                return false;
            },
        }),
        jumpModifierHint,
    ];
}

/**
 * Toggles `.cm-lsp-jump-ready` while Ctrl/Cmd is held. Modifier state is tracked on the window,
 * which is the only place that reports a release outside the editor.
 */
class JumpModifierValue implements PluginValue {
    private held = false;

    constructor(private readonly view: EditorView) {
        window.addEventListener('keydown', this.onKey);
        window.addEventListener('keyup', this.onKey);
        window.addEventListener('blur', this.onBlur);
    }

    private readonly onKey = (event: KeyboardEvent): void => {
        this.set(event.ctrlKey || event.metaKey);
    };

    private readonly onBlur = (): void => this.set(false);

    private set(held: boolean): void {
        if (held === this.held) return;
        this.held = held;
        this.view.dom.classList.toggle('cm-lsp-jump-ready', held);
    }

    destroy(): void {
        window.removeEventListener('keydown', this.onKey);
        window.removeEventListener('keyup', this.onKey);
        window.removeEventListener('blur', this.onBlur);
        this.view.dom.classList.remove('cm-lsp-jump-ready');
    }
}

const jumpModifierHint = [
    ViewPlugin.define(view => new JumpModifierValue(view)),
    EditorView.baseTheme({
        '&.cm-lsp-jump-ready .cm-content': { cursor: 'pointer' },
    }),
];

/**
 * Everything go-to-definition needs: the hover card and Ctrl/Cmd+Click, both reaching the server
 * through the editor's `LSPPlugin`.
 */
export function lspDefinitionActions(options: LspHoverActionOptions): Extension {
    return [lspHoverActions(options), lspDefinitionClick(options)];
}
