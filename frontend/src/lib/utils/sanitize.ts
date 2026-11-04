/**
 * The one place core turns markup someone else wrote into an HTML string it will render: chat and
 * tool output, agent descriptions, language-server documentation. `html-sinks.py` fails the gate when
 * a sink stops importing this module.
 *
 * The refusals below and why each is that shape: `docs/markup-sanitizing.md → The Sanitizer`.
 * `__tests__/sanitize.unit.ts` pins them as a payload list, measured on the pinned DOMPurify.
 */
import { Marked } from 'marked';
import DOMPurify from 'dompurify';
import { storeBounded } from '$lib/utils/bounded-map';

/** Attributes no untrusted fragment may carry, whatever their value. A mutable array only because
 * that is what DOMPurify's config type accepts. */
const FORBID_ATTR: string[] = ['style', 'id', 'name'];

/**
 * A class token a fragment may keep: the two color-only shapes a renderer this app feeds emits —
 * `marked`'s fence label, and the editor theme's class for a tag (`ͼ`-prefixed today). The attack
 * and the stylesheet gate beside it: `docs/markup-sanitizing.md → Class Adoption and the Stylesheet`.
 */
const KEPT_CLASS = /^(?:language-[\w.-]+|\u037c[\w-]+)$/;

/**
 * Drop every class token `KEPT_CLASS` does not cover. Per token, because one span carries the
 * classes for more than one tag.
 */
function keepRecognizedClasses(root: Element): void {
    for (const el of Array.from(root.querySelectorAll('[class]'))) {
        const kept = (el.getAttribute('class') ?? '').split(/\s+/).filter((cls) => KEPT_CLASS.test(cls));
        if (kept.length) el.setAttribute('class', kept.join(' '));
        else el.removeAttribute('class');
    }
}

/** Elements no untrusted fragment may carry, whatever they contain. Mutable for the same reason as
 * `FORBID_ATTR`; `style` is listed because DOMPurify keeps a nested one. */
const FORBID_TAGS: string[] = ['form', 'style'];

/**
 * Sanitize an HTML fragment authored outside this app.
 *
 * `forbidTags` adds to `FORBID_TAGS` for one call site's reason; it cannot re-allow anything.
 */
export function sanitizeHTML(html: string, forbidTags?: readonly string[]): string {
    const tags = forbidTags?.length ? [...FORBID_TAGS, ...forbidTags] : FORBID_TAGS;
    const body = DOMPurify.sanitize(html, {
        FORBID_ATTR, FORBID_TAGS: tags, ALLOW_DATA_ATTR: false, RETURN_DOM: true,
    }) as unknown as HTMLElement;
    keepRecognizedClasses(body);
    return body.innerHTML;
}

/** The one fence tag rendered as markup rather than shown; `DIAGRAM_FENCE_TAG` is drawn, as an image. */
const RENDERED_FENCE_TAG = 'markdown';

/** The first word of a fence's info string — ` ```js title=x ` gives `js`. `marked` keeps its case. */
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
 * Whether a fence holds a diagram source. A diagram becomes an image owned by a component, a rendered
 * fence becomes markup in this document.
 */
export function isDiagramFence(lang: string | undefined): boolean {
    return fenceTag(lang).toLowerCase() === DIAGRAM_FENCE_TAG;
}

/** Markdown parsed the default way, for inside a rendered fence. It has no renderer of its own, so a
 * nested `markdown` fence stays source. */
const plainMarkdown = new Marked({ async: false });

/** The parse a sink hands its text to. `false` is marked's "use the built-in renderer", and the nested
 * HTML lands in this output, so the one `sanitizeHTML` call covers a fenced document. */
const embeddingMarkdown = new Marked({ async: false }).use({
    renderer: {
        code({ lang, text }) {
            if (!isRenderedFence(lang)) return false;
            return plainMarkdown.parse(text, { async: false });
        },
    },
});

/**
 * How many rendered runs are remembered.
 *
 * @knipignore The cache-bound test measures this; nothing else in the app reads the number.
 */
export const MARKDOWN_RENDER_CACHE_MAX = 200;

/**
 * Rendered runs keyed by their source: a streaming answer asks for its finished paragraphs again on
 * every token flush, so anything the output starts to depend on has to join the key.
 */
const renderedMarkdown = new Map<string, string>();

/**
 * Markdown authored outside this app, rendered and sanitized in one step — what every chat-side sink
 * needs. `async: false` is required, not stylistic: without it `marked` returns a promise and the sink
 * stringifies `[object Promise]`.
 */
export function markdownToSafeHTML(markdown: string): string {
    const cached = renderedMarkdown.get(markdown);
    if (cached !== undefined) return cached;
    const html = sanitizeHTML(embeddingMarkdown.parse(markdown, { async: false }));
    storeBounded(renderedMarkdown, markdown, html, MARKDOWN_RENDER_CACHE_MAX);
    return html;
}

/** An SVG parsed as a standalone document must declare this. */
const SVG_NAMESPACE = 'http://www.w3.org/2000/svg';

/** Attributes the browser may follow somewhere else: another document, another host. */
const REFERRING_ATTRS = ['href', 'xlink:href', 'src'];

/**
 * A generated SVG *document* headed for an `<img>`, or `''` if it is not one — the only markup this app
 * keeps `style` on, because these bytes become an image of their own. Isolation model:
 * `docs/markup-sanitizing.md → Mermaid Diagrams`.
 */
export function svgImageMarkup(markup: string): string {
    if (!markup) return '';
    // DOMPurify's own defaults here, not the lists above: styles are the point of this markup.
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

/** A value headed for an inline `style` attribute, or `fallback` if it could start a declaration of
 * its own: inside that attribute a `;` begins another rule and `url()` is a fetch. */
export function safeCSSValue(value: string, fallback: string): string {
    return /[{};<>"'\\]|url\s*\(|expression\s*\(|@import/i.test(value) ? fallback : value;
}

/**
 * An icon file's contents, accepted only as a lone `<svg>` root — otherwise nothing. Every `Icon.svelte`
 * `src` today is a `?raw` import, and this check is what still holds if the next one comes from a
 * manifest instead.
 */
export function singleSvgMarkup(markup: string): string {
    if (!markup) return '';
    const body = DOMPurify.sanitize(markup, {
        FORBID_ATTR, FORBID_TAGS, ALLOW_DATA_ATTR: false, RETURN_DOM: true,
    }) as unknown as HTMLElement;
    keepRecognizedClasses(body);
    const kids = Array.from(body.children);
    if (kids.length !== 1 || kids[0].tagName.toLowerCase() !== 'svg') return '';
    // Text beside the root is a second payload riding along with the icon; text inside it is the
    // icon's own content and is inert.
    const beside = Array.from(body.childNodes).filter(n => n.nodeType === Node.TEXT_NODE);
    if (beside.some(n => (n.textContent ?? '').trim() !== '')) return '';
    return kids[0].outerHTML;
}
