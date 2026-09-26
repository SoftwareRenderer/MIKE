/**
 * The one place core asks for a diagram: a caller gets either a data URL for an `<img>` or the reason
 * there is none. `docs/markup-sanitizing.md → A diagram fence is drawn as an image` owns why.
 */
// Type-only: the runtime import inside `loadMermaid` is what keeps the renderer out of the app shell.
import type { MermaidConfig } from 'mermaid';
import { safeCSSValue, svgImageMarkup } from './sanitize';

/** Refused before the parser sees the text: a diagram this big is a mistake or an attack. */
const MAX_SOURCE_LENGTH = 20_000;

/** Cap on what the renderer may produce, applied before the markup becomes a URL. */
const MAX_MARKUP_LENGTH = 1_024 * 1_024;

/** Keys a diagram's own `%%{init}%%` directive may not change. */
const LOCKED_KEYS = [
    'securityLevel',
    'htmlLabels',
    'themeCSS',
    'themeVariables',
    'fontFamily',
    'suppressErrorRendering',
    'startOnLoad',
    'maxTextSize',
];

let mermaidLoader: Promise<typeof import('mermaid').default> | null = null;

let rendererLoaded = false;

/** The library scopes its generated stylesheet to this id and deletes any element already carrying it,
 * so no two renders may share one. */
let renderCount = 0;

/** Renders run one at a time: the library keeps its configuration in one module-level object, so a
 * second render can otherwise draw with the first one's `%%{init}%%` settings. */
let turns: Promise<unknown> = Promise.resolve();

/** The font the page is using, or `''` to let the library choose. An SVG loaded as an image resolves no
 * custom property of ours, so the stack travels as a literal, through `safeCSSValue`. */
function pageFontFamily(): string {
    if (typeof document === 'undefined') return '';
    return safeCSSValue(getComputedStyle(document.body).fontFamily, '');
}

function configuration(): MermaidConfig {
    const font = pageFontFamily();
    return {
        // Nothing is rendered until this module asks for it by name.
        startOnLoad: false,
        // Escaped label text: no click callbacks, no link URLs, no HTML in a `<foreignObject>`.
        securityLevel: 'strict',
        htmlLabels: false,
        // A diagram that does not parse throws instead of drawing an error picture of its own.
        suppressErrorRendering: true,
        theme: 'dark',
        maxTextSize: MAX_SOURCE_LENGTH,
        secure: LOCKED_KEYS,
        // The library copies this into `themeVariables` when they carry none.
        ...(font ? { fontFamily: font } : {}),
    };
}

async function loadMermaid(): Promise<typeof import('mermaid').default> {
    if (!mermaidLoader) {
        mermaidLoader = import('mermaid')
            .then((module) => {
                module.default.initialize(configuration());
                rendererLoaded = true;
                return module.default;
            })
            .catch((error: unknown) => {
                // A chunk that failed to fetch must not be remembered as the answer for later diagrams.
                mermaidLoader = null;
                throw error;
            });
    }
    return mermaidLoader;
}

/** The markup as the URL of an image: an SVG in an `<img>` is a document of its own. */
function imageDataUrl(markup: string): string {
    const bytes = new TextEncoder().encode(markup);
    let binary = '';
    // Chunked: handing every byte to one `fromCharCode` builds the string quadratically.
    for (let i = 0; i < bytes.length; i += 8192) {
        binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
    }
    return `data:image/svg+xml;base64,${btoa(binary)}`;
}

type DiagramFailure = 'too-large' | 'failed';

export type DiagramAnswer =
    | { ok: true; imageSrc: string; markup: string }
    | { ok: false; reason: DiagramFailure };

async function renderOnce(source: string): Promise<DiagramAnswer> {
    if (source.length > MAX_SOURCE_LENGTH) return { ok: false, reason: 'too-large' };
    // The library measures text by laying it out, so it needs a live element. This one is off-screen,
    // hidden from assistive technology, and removed whatever the render does.
    const stage = document.createElement('div');
    stage.setAttribute('aria-hidden', 'true');
    stage.style.position = 'fixed';
    stage.style.top = '0';
    stage.style.left = '-100vw';
    stage.style.visibility = 'hidden';
    stage.style.pointerEvents = 'none';
    document.body.appendChild(stage);
    try {
        const mermaid = await loadMermaid();
        const rendered = await mermaid.render(`mike-diagram-${++renderCount}`, source, stage);
        const markup = svgImageMarkup(rendered.svg);
        if (!markup || markup.length > MAX_MARKUP_LENGTH) return { ok: false, reason: 'failed' };
        return { ok: true, imageSrc: imageDataUrl(markup), markup };
    } catch {
        // A diagram that does not parse is the common case, and its message is not for the bubble.
        return { ok: false, reason: 'failed' };
    } finally {
        stage.remove();
    }
}

/** What this session has drawn, oldest first. The transcript hands a fence the same text again whenever a
 * row is redrawn, and starting from nothing would show the source where the picture stood. */
const drawn = new Map<string, DiagramAnswer>();

/** The render a second caller with the same text waits on. */
const pending = new Map<string, Promise<DiagramAnswer>>();

/** One drawing can cost megabytes, so the count alone is not the budget. */
const MAX_DRAWN_SOURCES = 100;
const MAX_DRAWN_BYTES = 8 * 1024 * 1024;
let drawnBytes = 0;

function weight(answer: DiagramAnswer): number {
    return answer.ok ? answer.imageSrc.length + answer.markup.length : 0;
}

function remember(source: string, answer: DiagramAnswer): void {
    const previous = drawn.get(source);
    if (previous) drawn.delete(source);
    drawn.set(source, answer);
    drawnBytes += weight(answer) - (previous ? weight(previous) : 0);
    while (drawn.size > MAX_DRAWN_SOURCES || drawnBytes > MAX_DRAWN_BYTES) {
        const oldest = drawn.keys().next();
        if (oldest.done) break;
        drawnBytes -= weight(drawn.get(oldest.value)!);
        drawn.delete(oldest.value);
    }
}

/** The picture this `source` already produced, or `null` when nothing has drawn it. Asking again keeps that
 * picture the newest in the memory. */
export function drawnDiagram(source: string): DiagramAnswer | null {
    const answer = drawn.get(source);
    if (!answer) return null;
    drawn.delete(source);
    drawn.set(source, answer);
    return answer;
}

async function drawOnce(source: string): Promise<DiagramAnswer> {
    const answer = await renderOnce(source);
    // A renderer that never arrived is the transport's failure, so the next call gets to retry it.
    if (answer.ok || rendererLoaded) remember(source, answer);
    return answer;
}

/** A diagram for `source`, or the reason there is none. Never throws. */
export function renderDiagram(source: string): Promise<DiagramAnswer> {
    const already = drawnDiagram(source);
    if (already) return Promise.resolve(already);
    const waiting = pending.get(source);
    if (waiting) return waiting;
    const turn = turns.then(() => drawOnce(source));
    turns = turn.catch(() => undefined);
    const settled = turn.finally(() => { pending.delete(source); });
    pending.set(source, settled);
    return settled;
}
