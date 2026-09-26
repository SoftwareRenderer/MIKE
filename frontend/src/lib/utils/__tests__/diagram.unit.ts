import { describe, it, expect, beforeAll } from 'vitest';
import { stubSvgMeasurement } from './svg-measurement';
import { renderDiagram, drawnDiagram } from '../diagram';

// The real renderer runs here, so these tests are about what the app is willing to put on screen: the
// shape of the answer, what a directive may not change, and what a failed render leaves behind.

// The first render pays for the lazy renderer chunk; here rather than in one test's budget.
beforeAll(async () => {
    stubSvgMeasurement();
    await renderDiagram('flowchart TD\n  A[warm up] --> B[the renderer]');
}, 60_000);

const FLOWCHART = ['flowchart TD', '  A[Start] --> B{Decide}', '  B -->|yes| C[Ship it]', 'classDef warn fill:#f99,stroke:#333;', 'class C warn'].join('\n');

/** The markup an `<img>` would be handed, out of a data URL. */
function decoded(imageSrc: string): string {
    const prefix = 'data:image/svg+xml;base64,';
    expect(imageSrc.startsWith(prefix)).toBe(true);
    const binary = atob(imageSrc.slice(prefix.length));
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
    return new TextDecoder().decode(bytes);
}

function stageCount(): number {
    return document.body.querySelectorAll('div[aria-hidden="true"]').length;
}

describe('renderDiagram', () => {
    it('answers with an image of the diagram', async () => {
        const answer = await renderDiagram(FLOWCHART);
        expect(answer.ok).toBe(true);
        if (!answer.ok) return;
        const markup = decoded(answer.imageSrc);
        expect(markup).toContain('Start');
        // A label is drawn as one `<tspan>` per wrapped line, so the word is what is asserted.
        expect(markup).toContain('Ship');
        // The copy control hands out this markup, so it must be the image's own document.
        expect(answer.markup).toBe(markup);
        expect(markup.startsWith('<svg')).toBe(true);
    });

    it('keeps the stylesheet that colors the diagram, and the diagram only', async () => {
        const answer = await renderDiagram(FLOWCHART);
        if (!answer.ok) throw new Error('expected a diagram');
        // The `classDef` rule above is the thing the chat sanitizer refuses, and the diagram needs it.
        expect(answer.markup).toContain('<style>');
        expect(answer.markup).toMatch(/warn\{|fill:#f99|#f99/);
    });

    it('hands over sanitized markup rather than what the renderer wrote', async () => {
        const answer = await renderDiagram(FLOWCHART);
        if (!answer.ok) throw new Error('expected a diagram');
        // The renderer styles its root with `max-width`; the image path refuses that and keeps the rest.
        expect(/<svg[^>]*>/.exec(answer.markup)?.[0]).not.toContain('style=');
        expect(answer.markup).toMatch(/<(rect|path)[^>]*style=/);
    });

    it('leaves nothing behind in the document', async () => {
        const before = stageCount();
        await renderDiagram(FLOWCHART);
        expect(stageCount()).toBe(before);
    });

    it('leaves nothing behind when the source does not parse', async () => {
        const before = stageCount();
        const answer = await renderDiagram('flowchart TD\n  A -->>--> B not a diagram');
        expect(answer).toEqual({ ok: false, reason: 'failed' });
        expect(stageCount()).toBe(before);
    });

    it('refuses a source too large to parse before parsing it', async () => {
        const answer = await renderDiagram(`flowchart TD\n${'  A[x] --> B[y]\n'.repeat(2000)}`);
        expect(answer).toEqual({ ok: false, reason: 'too-large' });
        expect(stageCount()).toBe(0);
    });

    it.each([
        ['the security posture of another diagram', '%%{init: {"securityLevel": "loose", "htmlLabels": true}}%%\n' + FLOWCHART],
        ['a stylesheet of its own', '%%{init: {"themeCSS": "body{display:none}#app{position:fixed;inset:0}"}}%%\n' + FLOWCHART],
        ['a font stack with a rule in it', '%%{init: {"fontFamily": "x}body{display:none}"}}%%\n' + FLOWCHART],
    ])('refuses a directive that would change %s', async (_name, source) => {
        const answer = await renderDiagram(source);
        expect(answer.ok).toBe(true);
        if (!answer.ok) return;
        // No HTML labels, and nothing in the stylesheet that is about anything but the diagram.
        expect(answer.markup).not.toMatch(/foreignObject|<div|<img/i);
        const styles = answer.markup.slice(answer.markup.indexOf('<style'), answer.markup.indexOf('</style>'));
        expect(styles).not.toMatch(/(^|})\s*(body|html|#app)\s*\{/);
        expect(styles).not.toContain('display:none');
        // The label the payload was hiding behind is still drawn as text.
        expect(answer.markup).toContain('Start');
    });

    it('keeps no HTML from a diagram that asks for HTML labels through a nested key', async () => {
        // `secure` refuses top-level keys only, so this one reaches the renderer; what is kept is SVG text.
        const answer = await renderDiagram('%%{init: {"flowchart": {"htmlLabels": true}}}%%\nflowchart TD\n  A["<img src=x onerror=alert(1)>"] --> B');
        expect(answer.ok).toBe(true);
        if (!answer.ok) return;
        expect(answer.markup).not.toMatch(/foreignObject|<div|<img|onerror/i);
        expect(answer.markup).toContain('img');
    });

    it('draws two diagrams asked for at the same time as two diagrams', async () => {
        const [one, two] = await Promise.all([
            renderDiagram('flowchart LR\n  A[Alpha] --> B[Beta]'),
            renderDiagram('flowchart LR\n  A[Gamma] --> D[Delta]'),
        ]);
        expect(one.ok && two.ok).toBe(true);
        if (!one.ok || !two.ok) return;
        expect(one.markup).toContain('Alpha');
        expect(one.markup).not.toContain('Delta');
        expect(two.markup).toContain('Gamma');
        expect(two.markup).not.toContain('Alpha');
        // Each diagram's stylesheet is scoped to its own id.
        const scope = (markup: string) => /#([\w-]+)\s*\{/.exec(markup)?.[1];
        expect(scope(one.markup)).toBeTruthy();
        expect(scope(one.markup)).not.toBe(scope(two.markup));
    });

    it('answers a source it has already drawn from that drawing', async () => {
        const source = 'flowchart LR\n  A[Once] --> B[Again]';
        expect(drawnDiagram(source)).toBeNull();
        const first = await renderDiagram(source);
        expect(first.ok).toBe(true);
        // The same object: a second render would scope its stylesheet to a new id and repaint the picture.
        expect(drawnDiagram(source)).toBe(first);
        expect(await renderDiagram(source)).toBe(first);
    });

    it('runs one render for a source several callers ask for at once', async () => {
        const source = 'flowchart LR\n  A[Twin] --> B[One]';
        const [one, two] = await Promise.all([renderDiagram(source), renderDiagram(source)]);
        expect(one).toBe(two);
    });

    it('remembers the reason a source was not drawn', async () => {
        const source = 'flowchart TD\n  A -->>--> B not a diagram twice';
        const answer = await renderDiagram(source);
        expect(answer).toEqual({ ok: false, reason: 'failed' });
        expect(drawnDiagram(source)).toEqual({ ok: false, reason: 'failed' });
    });
});
