/**
 * The shared "what must never survive sanitizing" scanner. `sanitize.unit.ts` holds it
 * against the sanitizer and the component sink suite holds it against the components, so
 * a new hazard class lands in both instead of drifting apart.
 *
 * A helper, not a suite: the vitest `include` pattern matches `*.unit.*` only, so nothing
 * here runs on its own.
 */

/** Attributes that execute or exfiltrate when they carry a scheme. */
const URI_ATTRS = ['href', 'src', 'action', 'data', 'formaction', 'xlink:href', 'srcset', 'poster', 'ping', 'codebase'];

/** A scheme that navigates whatever receives it, wherever it appears: an SVG animation's
 * `values` or `to`, a meta refresh's `content`. */
const NEVER_SCHEME = /^\s*(javascript|vbscript)\s*:/i;

/** Elements that execute, navigate, or embed a document of someone else's choosing. */
const EXECUTING_ELEMENTS = ['script', 'iframe', 'object', 'embed', 'base', 'form'];

/** `<meta http-equiv>` restates a response header from inside the document, so a refresh
 * navigates the page the fragment was inserted into. */
const NAVIGATING_ATTRIBUTES: Record<string, string[]> = { meta: ['http-equiv'] };

/** Elements that carry a stylesheet. A `<style>` runs nothing, but its rules can hide the
 * board or reach out with `url()`, and DOMPurify's defaults keep one nested inside a
 * container. */
const STYLESHEET_ELEMENTS = ['style'];

/**
 * Everything in `source` that untrusted markup must not be allowed to become. A string is
 * parsed into a detached element first, so the answer comes from the DOM the browser built
 * rather than from a pattern matched against the markup. `<template>` and `<slot>` are
 * deliberately not hazards: docs/markup-sanitizing.md.
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
            // The `style` attribute is this app's addition to DOMPurify's defaults.
            if (attr.name === 'style') found.push(`style=${attr.value}`);
            if (URI_ATTRS.includes(attr.name) && /^\s*(javascript|vbscript|data)\s*:/i.test(attr.value)) {
                found.push(`uri ${attr.name}=${attr.value}`);
            }
            if (NEVER_SCHEME.test(attr.value)) found.push(`scheme ${attr.name}=${attr.value}`);
            if ((NAVIGATING_ATTRIBUTES[tag] ?? []).includes(attr.name)) {
                found.push(`navigating ${tag}[${attr.name}=${attr.value}]`);
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
