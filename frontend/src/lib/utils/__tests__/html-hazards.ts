/**
 * The shared "what must never survive sanitizing" scanner.
 *
 * Two suites need the same answer — `sanitize.unit.ts` holds it against the sanitizer
 * itself, `chat-sink-sanitization.unit.svelte.ts` against the components that call it —
 * and one definition is the point: a hazard class added here (a new URI attribute, a new
 * executing element) is then refused in both suites at once, where two hand-copied
 * scanners would drift and one of them would keep printing a green line.
 *
 * This is a helper, not a suite: the vitest `include` pattern matches `*.unit.*` only,
 * so nothing here runs on its own.
 */

/** Attributes that execute or exfiltrate when they carry a scheme. */
const URI_ATTRS = ['href', 'src', 'action', 'data', 'formaction', 'xlink:href'];

/** Elements that execute, navigate, or embed a document of someone else's choosing. */
const EXECUTING_ELEMENTS = ['script', 'iframe', 'object', 'embed', 'base', 'form'];

/** Elements that carry a stylesheet. A `<style>` runs nothing and is not in DOMPurify's
 * defaults-refused set — it survives nested inside a container — so it is named here
 * rather than assumed absent: rules written by a model can hide the board, restyle a
 * control into looking pressed, or reach out with `url()`. The `style` **attribute** is
 * reported below; without this the two suites would keep printing a green line with a
 * stylesheet in the DOM, which is exactly what happened before it was measured. */
const STYLESHEET_ELEMENTS = ['style'];

/**
 * Everything in `source` that untrusted markup must not be allowed to become.
 * A string is parsed into a detached element first, so the answer comes from the
 * DOM the browser built rather than from a pattern matched against the markup.
 */
export function htmlHazards(source: string | Element): string[] {
    const scope = typeof source === 'string' ? parse(source) : source;
    const found: string[] = [];
    for (const el of Array.from(scope.querySelectorAll('*'))) {
        const tag = el.tagName.toLowerCase();
        if (EXECUTING_ELEMENTS.includes(tag)) found.push(`element <${tag}>`);
        if (STYLESHEET_ELEMENTS.includes(tag)) found.push(`stylesheet <${tag}>`);
        for (const attr of Array.from(el.attributes)) {
            if (/^on/i.test(attr.name)) found.push(`handler ${attr.name}`);
            // `style` is the refusal this app adds to DOMPurify's defaults, so it is
            // reported here rather than assumed absent.
            if (attr.name === 'style') found.push(`style=${attr.value}`);
            if (URI_ATTRS.includes(attr.name) && /^\s*(javascript|vbscript|data)\s*:/i.test(attr.value)) {
                found.push(`uri ${attr.name}=${attr.value}`);
            }
        }
    }
    return found;
}

function parse(html: string): Element {
    const box = document.createElement('div');
    box.innerHTML = html;
    return box;
}
