/**
 * LSP hover card with a clickable "Go to Definition" action.
 *
 * Not a `hoverTooltip()` source: CodeMirror dismisses a hover as soon as the pointer leaves the hovered
 * position (±4px around the box), so a tooltip carrying a button closes during the motion needed to
 * press it. A promise-returning source is late too — nothing shows until the server answers, by which
 * time the pointer has started moving. So open and close are ours:
 *
 *   • opens after `hoverTime` (500 ms) of stillness on an identifier. Every card pays it: moving to
 *     another identifier closes this one and starts a fresh dwell, so a card never appears for a symbol
 *     the pointer only passed over.
 *   • stays open while the pointer is on the identifier or inside the card, and for `graceMs` after it
 *     leaves both — the time needed to cross the gap and press the button. Coming back to the
 *     identifier inside that window re-arms the dwell, because the card on screen is about a symbol
 *     that was left. A wiggle inside the identifier is not a new hover: `mousemove` fires for a single
 *     pixel, and restarting there would make the dwell impossible to reach.
 *   • closes on a scroll the pointer is not inside, and on any doc change.
 *
 * `reduceHover` below makes these decisions; the view class is timers, DOM and dispatch.
 */

import { EditorView, ViewPlugin, showTooltip, type PluginValue, type Tooltip, type TooltipView, type ViewUpdate } from '@codemirror/view';
import { StateEffect, StateField, type Extension, type Text } from '@codemirror/state';
import DOMPurify from 'dompurify';
import { LSPPlugin } from '@codemirror/lsp-client';
import { inBox } from './hit-box';
import { identifierRange, pickDefinitionTarget, toLspPosition, type CharRange, type DefinitionTarget, type LspPosition } from './lsp-definition';

// ─── Pure state machine ──────────────────────────────────────────────────────

/**
 * Where the pointer is relative to the card. `'other-token'` is an identifier other than the card's, and
 * it closes the card immediately — which a geometry check cannot tell from a pointer heading for the card.
 */
export type HoverZone = 'token' | 'tooltip' | 'other-token' | 'outside';

export type HoverPhase = 'idle' | 'arming' | 'open';

export interface HoverMachineState {
    readonly phase: HoverPhase;
    /** Identifier the card is (or will be) about. */
    readonly token: CharRange | null;
    /** True while the close-grace timer is running. */
    readonly gracePending: boolean;
}

export const HOVER_IDLE: HoverMachineState = { phase: 'idle', token: null, gracePending: false };

export type HoverInput =
    | { type: 'pointermove'; zone: HoverZone; token: CharRange | null }
    | { type: 'hover-elapsed' }
    | { type: 'grace-elapsed' }
    | { type: 'scroll'; zone: HoverZone }
    | { type: 'doc-change' }
    /** The card turned out to have nothing to show, or the connection went away. */
    | { type: 'dismiss' };

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
 * Start the dwell for `token`. A card already on screen is about a symbol the pointer left, so it closes
 * rather than being re-targeted: every card pays `hoverTime`, not just the first of a session.
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
 * Next state + the timer/UI side effects the caller must carry out.
 *
 * `token` on a `pointermove` is the identifier *under the pointer* (null when
 * there is none), so the machine never has to guess.
 */
export function reduceHover(state: HoverMachineState, input: HoverInput): HoverDecision {
    switch (input.type) {
        case 'pointermove': {
            const { zone, token } = input;
            if (zone === 'tooltip') {
                // Only meaningful while open; a stray move there otherwise means
                // the pointer left the identifier, so stop arming.
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
            // Open, and the pointer is not on the card's identifier: another identifier is a different
            // question, and this one was already left once. Either way, dwell again.
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

// The pointer-vs-card hit test lives in `hit-box.ts`, shared with
// `closeHoverOnScroll` so both agree on when a tooltip is still being pointed at.

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
    // MarkupContent carries its own kind: plaintext must stay escaped, not be
    // run through the markdown renderer.
    if ('kind' in item && item.kind) return plugin.docToHTML(item as { kind: 'markdown' | 'plaintext'; value: string });
    if ('language' in item && item.language) {
        return plugin.docToHTML({ kind: 'markdown', value: fenced(item.language, item.value ?? '') }, 'markdown');
    }
    if ('value' in item) return plugin.docToHTML(String(item.value ?? ''), 'markdown');
    return '';
}

/** `Hover.contents` (string | MarkupContent | MarkedString[]) to sanitized HTML. */
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

    return DOMPurify.sanitize(html, { FORBID_TAGS: ['hr'] });
}

/**
 * The card's DOM. Content is filled in as the two requests land, and the
 * action row is the *bottom* section: with `above: true` the card keeps its
 * bottom edge anchored to the code line, so late-arriving documentation grows
 * upward and the button never moves out from under the pointer.
 */
export class HoverCard {
    readonly dom = document.createElement('div');
    private readonly loading: HTMLElement;
    private readonly docs: HTMLElement;
    private readonly row: HTMLElement;
    private readonly button: HTMLButtonElement;
    private destroyed = false;

    constructor(onJump: (target: DefinitionTarget) => void) {
        this.dom.className = 'cm-lsp-action-card';
        const body = document.createElement('div');
        body.className = 'cm-lsp-action-card-body';
        this.loading = document.createElement('div');
        this.loading.className = 'cm-lsp-action-loading';
        this.loading.textContent = '…';
        this.docs = document.createElement('div');
        this.docs.className = 'cm-lsp-action-docs cm-lsp-documentation';
        this.docs.hidden = true;
        body.append(this.loading, this.docs);

        this.row = document.createElement('div');
        this.row.className = 'cm-lsp-action-row';
        this.row.hidden = true;
        this.button = document.createElement('button');
        this.button.type = 'button';
        this.button.className = 'cm-lsp-action-button';
        this.button.textContent = 'Go to Definition';
        this.button.addEventListener('click', e => {
            e.preventDefault();
            e.stopPropagation();
            const target = this.target;
            if (target) onJump(target);
        });
        // A press on the card is not a press on the text underneath it: the card
        // overlaps the code, and without this CodeMirror would move the caret to
        // whatever sits below the button.
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
        this.loading.hidden = true;
        if (!html) return;
        this.docs.innerHTML = html;
        this.docs.hidden = false;
    }

    showAction(target: DefinitionTarget): void {
        if (this.destroyed) return;
        this.target = target;
        this.row.hidden = false;
    }

    /** Resolved with nothing to show — collapse the placeholder. */
    hideLoading(): void {
        if (this.destroyed) return;
        this.loading.hidden = true;
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
    /** Whether a definition target can be jumped to from this editor. */
    canJump?: (target: DefinitionTarget) => boolean;
    /** Perform the jump. */
    onJump: (target: DefinitionTarget) => void;
}

// How long the pointer has to rest on an identifier before anything appears
export const DEFAULT_HOVER_TIME = 500;

// How long the card survives after the pointer leaves it and the identifier
export const DEFAULT_HOVER_GRACE = 500;

export function resolveHoverTimings(
    options: Pick<LspHoverActionOptions, 'hoverTime' | 'graceMs'>,
): { hoverTime: number; graceMs: number } {
    return {
        hoverTime: options.hoverTime ?? DEFAULT_HOVER_TIME,
        graceMs: options.graceMs ?? DEFAULT_HOVER_GRACE,
    };
}

interface ResolvedHover {
    docs: string;
    definition: DefinitionTarget | null;
}

/** `TextDocumentPositionParams`, shaped locally to avoid a protocol dependency. */
interface HoverRequestParams {
    textDocument: { uri: string };
    position: LspPosition;
}

const CACHE_LIMIT = 200;

/**
 * Per-view request memos, shared by the hover card and the click path so a
 * click straight after a hover (or the other way round) costs no round trip.
 * Keys are document offsets, so any edit clears them.
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

function remember<T>(cache: Map<string, Promise<T>>, key: string, make: () => Promise<T>): Promise<T> {
    const existing = cache.get(key);
    if (existing) return existing;
    const promise = make();
    cache.set(key, promise);
    if (cache.size > CACHE_LIMIT) {
        const oldest = cache.keys().next();
        if (!oldest.done) cache.delete(oldest.value);
    }
    // A timeout must stay retryable: drop it instead of remembering "nothing".
    promise.catch(() => {
        if (cache.get(key) === promise) cache.delete(key);
    });
    return promise;
}

function tokenKey(plugin: LSPPlugin, token: CharRange): string {
    return `${plugin.uri}\u0000${token.from}:${token.to}`;
}

/**
 * Capability probe. `hasCapability` is missing from @codemirror/lsp-client
 * 6.2.5's declarations, so read the public record: capabilities that have not
 * arrived yet must not block a request, a known absence must.
 */
function supported(plugin: LSPPlugin, cap: 'hoverProvider' | 'definitionProvider'): boolean {
    const caps = plugin.client.serverCapabilities;
    return caps ? !!caps[cap] : true;
}

/** Hover documentation, HTML-escaped/sanitized. */
function fetchDocs(plugin: LSPPlugin, token: CharRange, doc: Text): Promise<string> {
    if (!supported(plugin, 'hoverProvider')) return Promise.resolve('');
    // Flush unsaved edits first: the server must answer about this buffer.
    plugin.client.sync();
    const params: HoverRequestParams = { textDocument: { uri: plugin.uri }, position: toLspPosition(doc, token.from) };
    return plugin.client
        .request<HoverRequestParams, RawMarkup | null>('textDocument/hover', params)
        .then(hover => hoverDocsHTML(plugin, hover));
}

function fetchDefinition(plugin: LSPPlugin, token: CharRange, doc: Text): Promise<DefinitionTarget | null> {
    if (!supported(plugin, 'definitionProvider')) return Promise.resolve(null);
    plugin.client.sync();
    const params: HoverRequestParams = { textDocument: { uri: plugin.uri }, position: toLspPosition(doc, token.from) };
    return plugin.client
        .request<HoverRequestParams, unknown>('textDocument/definition', params)
        .then(result => pickDefinitionTarget(result));
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
    private lastPlugin: LSPPlugin | null = null;
    /** Last pointer position, used to classify a scroll. */
    private pointer: { x: number; y: number } | null = null;
    /**
     * `EditorView.update` throws if a plugin dispatches while it runs, so a
     * close requested from inside `update` is handed to the next task instead.
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
                // Cache keys are document offsets — an edit makes every one of them stale.
                clearCaches(update.view);
                this.feed({ type: 'doc-change' });
            }
            const plugin = LSPPlugin.get(update.view);
            if (plugin !== this.lastPlugin) {
                // Different file/connection: everything cached belongs to the old one.
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
        // Leaving for the card is a move, not a leave: the card lives inside
        // view.dom, so only a truly departed pointer (or one over other text)
        // reaches this branch via zoneAt.
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
    }

    // ---- card ----

    private open(token: CharRange): void {
        const plugin = LSPPlugin.get(this.view);
        if (!plugin) {
            this.feed({ type: 'dismiss' });
            return;
        }
        // Nothing should be on screen when a dwell elapses; anything that is gets destroyed here
        // rather than left behind as a second tooltip.
        this.card?.destroy();
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
        void this.fill(plugin, token, card, seq);
    }

    private closeCard(): void {
        this.card?.destroy();
        this.card = null;
        // A doc change already dropped the card in the field — nothing to remove.
        if (!this.view.state.field(hoverCardField)) return;
        if (this.inUpdate) {
            setTimeout(() => this.removeCard(), 0);
        } else {
            this.removeCard();
        }
    }

    private removeCard(): void {
        // A doc change already dropped the card in the field; the view may also
        // be gone by the time a deferred close runs.
        if (this.destroyed || !this.view.state.field(hoverCardField)) return;
        this.view.dispatch({ effects: setHoverCard.of(null) });
    }

    private async fill(plugin: LSPPlugin, token: CharRange, card: HoverCard, seq: number): Promise<void> {
        const live = () => !card.isDestroyed && seq === this.seq && this.machine.phase === 'open';
        let resolved: ResolvedHover;
        try {
            resolved = await this.resolve(plugin, token);
        } catch {
            if (live()) this.feed({ type: 'dismiss' });
            return;
        }
        if (!live()) return;
        card.showDocs(resolved.docs);
        const target = resolved.definition;
        const jumpable = !!target && (this.opts.canJump ? this.opts.canJump(target) : true);
        if (target && jumpable) card.showAction(target);
        if (!resolved.docs && !jumpable) {
            // Nothing to say about this identifier — do not park an empty box on it.
            this.feed({ type: 'dismiss' });
        } else {
            card.hideLoading();
        }
    }

    /** Per-token memo: repeat hovers are free, and the machine never refires. */
    private resolve(plugin: LSPPlugin, token: CharRange): Promise<ResolvedHover> {
        const caches = cachesFor(this.view);
        const key = tokenKey(plugin, token);
        const doc = this.view.state.doc;
        return Promise.all([
            remember(caches.docs, key, () => fetchDocs(plugin, token, doc)),
            remember(caches.definition, key, () => fetchDefinition(plugin, token, doc)),
        ]).then(([docs, definition]) => ({ docs, definition }));
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
    '& .cm-lsp-action-loading': { color: 'var(--color-text-muted, #888)', padding: '1px 2px' },
    // NOT `white-space: pre-wrap`: the renderer's output is block HTML, and it
    // writes a newline *between* every tag (`</pre>\n<p>`), which pre-wrap turns
    // into a blank line between the signature and its prose — and between every
    // paragraph after it. Line structure that matters survives without it:
    // plaintext documentation arrives as `<br>`s and code blocks keep their own
    // pre-wrap (next rule).
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
 * Hover card for the file's language server. Finds the connection through
 * `LSPPlugin`, so it survives an LSP reconnect. One half of
 * `lspDefinitionActions`, which is this module's entry point.
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
 * Definition of the identifier at `pos`, sharing the hover card's cache.
 * Resolves to null when there is no identifier, no connection, or the server
 * has nothing to say — a jump is never attempted on a guess.
 */
function definitionAt(view: EditorView, pos: number): Promise<DefinitionTarget | null> {
    const plugin = LSPPlugin.get(view);
    if (!plugin) return Promise.resolve(null);
    const token = identifierRange(view.state.doc, pos);
    if (!token) return Promise.resolve(null);
    if (!supported(plugin, 'definitionProvider')) return Promise.resolve(null);
    const doc = view.state.doc;
    return remember(cachesFor(view).definition, tokenKey(plugin, token), () =>
        fetchDefinition(plugin, token, doc),
    ).catch(() => null);
}

/** Modifier combination that turns a click into a jump (VS Code's gesture). */
export function isJumpModifier(event: { ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }): boolean {
    // Shift/Alt are taken by selection and multi-cursor semantics.
    return (event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey;
}

/**
 * Ctrl/Cmd+Click jumps to the definition of the identifier under the pointer.
 * This is the primary gesture precisely because it has no target to hit: if a
 * hover card is being stubborn, the same jump is still one click away.
 *
 * The click is left uncanceled, so the caret still lands where it was clicked
 * (as in VS Code) and the explorer's line-selection keeps working.
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
                // No `canJump` gate here on purpose: the jump itself explains why
                // a target outside this workspace cannot be opened, and a silent
                // no-op is what this bug report was about.
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
 * Toggles `.cm-lsp-jump-ready` on the editor while Ctrl/Cmd is held so the
 * pointer hints that clicking jumps. Modifier state is tracked on the window:
 * the browser reports a release that happens outside the editor only there,
 * and a hint stuck on forever is worse than no hint.
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
 * Everything go-to-definition needs: the hover card and Ctrl/Cmd+Click. Both
 * talk to the language server through the editor's `LSPPlugin`, so they follow
 * the connection wherever the caller puts it.
 */
export function lspDefinitionActions(options: LspHoverActionOptions): Extension {
    return [lspHoverActions(options), lspDefinitionClick(options)];
}
