import { test, expect, type Browser, type Page } from '@playwright/test';
import { writeFileSync, appendFileSync, existsSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, apiGetConfig, createTaskViaApi, waitForReaction } from './helpers';

/**
 * The editor's go-to-definition UX.
 *
 * Keeping the card open is the hard half: the pointer has to travel from the identifier to the
 * card's button, and every gesture that dismisses a hover on leaving the text closes it before the
 * click lands. So open and close are the card's own, and these tests move the pointer in small
 * steps — the deliberate slow move.
 *
 * The tests drive a REAL browser with real layout (jsdom has neither).
 *
 * No language server is involved: `/api/extensions` is stubbed with a fake Go
 * LSP extension so the editor believes one is installed, and `/api/lsp` is
 * served by a scripted LSP over a `WebSocket` wrapper (Playwright cannot
 * intercept WebSockets with `page.route`, so the reconnect specs use the same
 * technique). The editor exercises its real LSP path: handshake, capabilities,
 * didOpen, hover, definition.
 *
 * Isolation: dedicated repo + project, per the heavy-fixture rule — this spec
 * never touches the shared /tmp/kanban-test-repo.
 */

const HOVER_REPO = '/tmp/kanban-test-repo-hover';
const CALLER = 'caller.go';
const TARGET = 'target.go';

// caller.go — `Helper` is defined in this file, `Alpha` in target.go, and
// `Documented` is the symbol the fake gives long, rule-filled documentation.
// `Stalled` is the one the fake never answers and `LateDoc` the one it answers late.
//   1 package hover
//   2
//   3 func Helper() int { return 2 }
//   4
//   5 func Use() int {
//   6     return Helper() + Alpha()
//   7 }
//   8
//   9 func Documented() int { return 3 }
//  10
//  11 func Stalled() int { return 4 }
//  12
//  13 func LateDoc() int { return 5 }
const CALLER_SRC = `package hover

func Helper() int { return 2 }

func Use() int {
\treturn Helper() + Alpha()
}

func Documented() int { return 3 }

func Stalled() int { return 4 }

func LateDoc() int { return 5 }
`;

const TARGET_SRC = `package hover

func Alpha() int { return 1 }
`;

// ─── Fake language server (runs in the page) ────────────────────────────────

/**
 * Installs a `WebSocket` wrapper: `/api/lsp` is answered by a scripted server,
 * every other socket (the board stream, terminals) is passed through to the
 * real implementation untouched.
 *
 * It keeps the text of every `didOpen`, so a definition request is answered by
 * reading the identifier at the requested position out of the document — the
 * way a real server does it — rather than by hard-coded offsets.
 */
const FAKE_LSP_INIT = (targetPath: string) => {
    const w = window as any;
    if (w.__fakeLSPPrepared) return;
    w.__fakeLSPPrepared = true;

    const RealWebSocket = w.WebSocket;
    const WORD = /[A-Za-z0-9_$]/;
    const TARGET_URI = 'file://' + targetPath;

    /**
     * Documentation with the shape a real server sends for a documented symbol:
     * a signature block, then prose in sections split by `---` (markdown rules),
     * taller than the card itself. The `---` stay in the fixture because the card
     * renders none of them.
     */
    const PARAGRAPH =
        'Documents the symbol at enough length that the documentation itself has to scroll inside the card, ' +
        'which is exactly the situation the pinned action row exists for. ';
    const section = (n: number) =>
        Array.from({ length: n }, (_, i) => `Section ${i + 1}. ${PARAGRAPH}${PARAGRAPH}`).join('\n\n');
    const LONG_DOC =
        '```go\nfunc Documented() int\n```\n\n---\n\n' + section(4) + '\n\n---\n\n' + section(4) + '\n\n---\n';

    /**
     * `LateDoc` answers documentation after LATE_DOC_MS and its definition after LATE_DEF_MS; that
     * order is the fixture. They live here because an init script is serialized into the page and a
     * module constant is not.
     */
    const LATE_DOC_MS = 2500;
    const LATE_DEF_MS = 6000;

    class FakeSocket extends EventTarget {
        /** Only `readyState`, `send`, `close`, `onmessage` and the event target
         *  are used — that is the whole surface the LSP connection manager
         *  touches (`addEventListener('open'|'close'|'error')` + `onmessage`). */
        readyState = 0;
        url: string;
        onmessage: ((ev: MessageEvent) => void) | null = null;
        /** Docs the editor opened, keyed by uri — the fake's only "index". */
        private docs = new Map<string, string>();

        constructor(url: string) {
            super();
            this.url = url;
            setTimeout(() => {
                this.readyState = 1;
                this.dispatchEvent(new Event('open'));
            }, 0);
        }

        send(raw: string): void {
            let msg: any;
            try {
                msg = JSON.parse(raw);
            } catch {
                return;
            }
            w.__fakeLSP.log.push({ method: msg.method });
            this.handle(msg);
        }

        close(): void {
            this.readyState = 3;
            this.dispatchEvent(new Event('close'));
        }

        private post(payload: unknown): void {
            const text = JSON.stringify(payload);
            if (this.readyState !== 1) return;
            const event = new MessageEvent('message', { data: text });
            this.onmessage?.(event);
            this.dispatchEvent(event);
        }

        private reply(msg: any, result: unknown): void {
            this.post({ jsonrpc: '2.0', id: msg.id, result, ...(msg._ch != null ? { _ch: msg._ch } : {}) });
        }

        private notify(method: string, params?: unknown, ch?: number): void {
            this.post({ jsonrpc: '2.0', method, params, ...(ch != null ? { _ch: ch } : {}) });
        }

        /** Identifier at an LSP position, exactly as a server would read it. */
        private wordAt(uri: string, line: number, character: number): string {
            const text = this.docs.get(uri);
            if (!text) return '';
            const lineText = text.split('\n')[line] ?? '';
            let a = Math.min(character, lineText.length);
            let b = a;
            while (a > 0 && WORD.test(lineText[a - 1])) a--;
            while (b < lineText.length && WORD.test(lineText[b])) b++;
            return lineText.slice(a, b);
        }

        private handle(msg: any): void {
            const params = msg.params ?? {};
            switch (msg.method) {
                case 'initialize':
                    this.reply(msg, {
                        capabilities: {
                            hoverProvider: true,
                            definitionProvider: true,
                            documentSymbolProvider: true,
                            textDocumentSync: 1,
                            completionProvider: { triggerCharacters: [] },
                        },
                        serverInfo: { name: 'fakegopls', version: '0.0.1' },
                    });
                    // The backend tells the editor when the server is warm.
                    this.notify('$/lspReady', undefined, msg._ch);
                    return;
                case 'shutdown':
                    this.reply(msg, null);
                    return;
                case 'textDocument/didOpen':
                    this.docs.set(params.textDocument.uri, params.textDocument.text ?? '');
                    return;
                case 'textDocument/didChange': {
                    const change = params.contentChanges?.[0];
                    if (change && typeof change.text === 'string') this.docs.set(params.textDocument.uri, change.text);
                    return;
                }
                case 'textDocument/didClose':
                    this.docs.delete(params.textDocument.uri);
                    return;
                case 'textDocument/hover': {
                    const { line, character } = params.position;
                    const word = this.wordAt(params.textDocument.uri, line, character);
                    if (word === 'Stalled') {
                        // Nothing answers it; only the card's own ceiling ends the wait.
                        return;
                    }
                    if (word === 'LateDoc') {
                        setTimeout(() => this.reply(msg, { contents: { kind: 'markdown', value: '```go\nfunc LateDoc() int\n```\n\nThe documentation arrived while the definition was still being computed.' } }), LATE_DOC_MS);
                        return;
                    }
                    if (word === 'Documented') {
                        this.reply(msg, { contents: { kind: 'markdown', value: LONG_DOC } });
                    } else if (word === 'Helper' || word === 'Alpha') {
                        this.reply(msg, { contents: { kind: 'markdown', value: '```go\nfunc ' + word + '() int\n```' } });
                    } else {
                        // Anything else has neither documentation nor a definition,
                        // so the card must not park an empty box on it.
                        this.reply(msg, null);
                    }
                    return;
                }
                case '$/cancelRequest':
                    // The log is how a test sees that an abandoned hover was cancelled.
                    return;
                case 'textDocument/definition': {
                    const { line, character } = params.position;
                    const uri = params.textDocument.uri;
                    const word = this.wordAt(uri, line, character);
                    if (word === 'Stalled') {
                        // Neither half is answered, so an abandoned hover has two requests to
                        // cancel.
                        return;
                    }
                    if (word === 'LateDoc') {
                        // The definition is the slow half of the two, so the documentation has to be
                        // on screen before this lands.
                        setTimeout(() => this.reply(msg, [{ targetUri: uri, targetRange: { start: { line: 12, character: 5 }, end: { line: 12, character: 12 } }, targetSelectionRange: { start: { line: 12, character: 5 }, end: { line: 12, character: 12 } } }]), LATE_DEF_MS);
                        return;
                    }
                    if (word === 'Alpha') {
                        this.reply(msg, [{ targetUri: TARGET_URI, targetRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 10 } }, targetSelectionRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 10 } } }]);
                    } else if (word === 'Helper') {
                        // Same-file definition (line 3, 1-based).
                        this.reply(msg, [{ targetUri: uri, targetRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 11 } }, targetSelectionRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 11 } } }]);
                    } else if (word === 'Documented') {
                        // Same-file, and far enough down that a tall card must keep its button on screen.
                        this.reply(msg, [{ targetUri: uri, targetRange: { start: { line: 8, character: 5 }, end: { line: 8, character: 15 } }, targetSelectionRange: { start: { line: 8, character: 5 }, end: { line: 8, character: 15 } } }]);
                    } else {
                        this.reply(msg, []);
                    }
                    return;
                }
                case 'textDocument/documentSymbol':
                    this.reply(msg, []);
                    return;
                default:
                    if (msg.id != null) this.reply(msg, null);
            }
        }
    }

    w.__fakeLSP = { log: [] as { method?: string }[] };

    function Wrapped(this: unknown, url: string | URL, protocols?: string | string[]): WebSocket {
        const target = String(url);
        if (target.includes('/api/lsp')) return new FakeSocket(target) as unknown as WebSocket;
        return protocols === undefined ? new RealWebSocket(target) : new RealWebSocket(target, protocols);
    }
    Wrapped.prototype = RealWebSocket.prototype;
    Object.setPrototypeOf(Wrapped, RealWebSocket);
    w.WebSocket = Wrapped as unknown as typeof WebSocket;
};

/** Installed-extension stub: one enabled LSP extension claiming `.go`. */
const fakeExtensionBody = () => [
    {
        id: 'fake-gopls',
        name: 'fakegopls',
        author: 'test',
        version: '0.0.1',
        type: 'lsp',
        enabled: true,
        running: true,
        install_source: 'test',
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '2026-01-01T00:00:00Z',
        config: {},
        container_config: {},
        sandbox_config: {},
        manifest: {
            name: 'fakegopls',
            version: '0.0.1',
            capabilities: {
                lsp: {
                    language: 'Go',
                    // The browser routes `.go` through `file_types` alone: a stub with only the legacy
                    // `extensions` list builds no LSPClient. The legacy key stays because real manifests
                    // still carry it (normalized server-side).
                    extensions: ['go'],
                    file_types: [{ extension: 'go', language_id: 'go', label: 'Go' }],
                    editor_enabled: true,
                    tool_calls_enabled: false,
                },
            },
        },
    },
];

// ─── Helpers ─────────────────────────────────────────────────────────────────

async function setupPage(browser: Browser) {
    const context = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
    const page = await context.newPage();
    await page.goto('/');
    return { context, page };
}

/** Stub the extension list and fake the LSP socket for this page. */
async function armFakeLSP(page: Page) {
    await page.addInitScript(FAKE_LSP_INIT, join(HOVER_REPO, TARGET));
    await page.route('**/api/extensions', route =>
        route.fulfill({ contentType: 'application/json', body: JSON.stringify(fakeExtensionBody()) }),
    );
}

/** Viewport coordinates of the middle of `needle` in the open document. */
async function coordsOf(page: Page, needle: string) {
    const box = await page.evaluate((token: string) => {
        const el = document.querySelector('.cm-editor') as any;
        const view = el?.__view;
        if (!view) return null;
        const pos = view.state.doc.toString().indexOf(token);
        if (pos < 0) return null;
        const a = view.coordsAtPos(pos + 1);
        const b = view.coordsAtPos(pos + token.length - 1);
        if (!a || !b) return null;
        return { left: a.left, right: b.right, top: a.top, bottom: b.bottom };
    }, needle);
    if (!box) throw new Error(`token not found / not rendered: ${needle}`);
    return { x: (box.left + box.right) / 2, y: (box.top + box.bottom) / 2 };
}

/** A point inside the editor but away from any text — blank space past EOL. */
async function blankSpot(page: Page) {
    return page.evaluate(() => {
        const el = document.querySelector('.cm-editor') as any;
        const view = el?.__view;
        if (!view) return null;
        const rect = view.dom.getBoundingClientRect();
        // Past the end of the last line's text, not below it: a "blank" spot that lands on an
        // identifier turns the move into a re-target, which is a different test.
        const at = view.coordsAtPos(view.state.doc.line(view.state.doc.lines).to);
        if (!at) return null;
        return { x: Math.min(rect.right - 8, at.right + 80), y: Math.max(rect.top + 8, at.bottom - 6) };
    });
}

/**
 * Wait until the fake server has seen the document open (LSP is live).
 *
 * The reported value names the cause, because "no didOpen" is three failures apart: a stub the browser
 * cannot route, a socket that was never faked, or a channel that opened and stayed silent.
 */
async function waitForLSPReady(page: Page) {
    await expect
        .poll(
            async () =>
                page.evaluate(() => {
                    const log = ((window as any).__fakeLSP?.log ?? []) as { method?: string }[];
                    if (log.some(m => m.method === 'textDocument/didOpen')) return 'opened';
                    const seen = log.map(m => m.method).slice(0, 5).join(', ');
                    return seen
                        ? `no didOpen; fake LSP saw: ${seen}`
                        : 'no didOpen and nothing else either: no LSPClient was built, so the '
                          + '/api/extensions stub is not routing .go (it must declare file_types)';
                }),
            { timeout: 20000 },
        )
        .toBe('opened');
}

const CARD = '.cm-lsp-action-card';
const CARD_BUTTON = '.cm-lsp-action-button';

// ─── Suite ───────────────────────────────────────────────────────────────────

// serial: the dedicated repo/project is created once for the whole suite.
test.describe.serial('Editor go-to-definition hover + Ctrl/Cmd+Click', () => {
    let projectId = '';

    // The suite answers `/api/extensions` itself, so an installed-server gate could only ever
    // answer "skip"; `waitForLSPReady` is the precondition.

    test.beforeAll(async ({ browser }) => {
        rmSync(HOVER_REPO, { recursive: true, force: true });
        mkdirSync(HOVER_REPO, { recursive: true });
        writeFileSync(join(HOVER_REPO, CALLER), CALLER_SRC);
        writeFileSync(join(HOVER_REPO, TARGET), TARGET_SRC);
        writeFileSync(join(HOVER_REPO, 'README.md'), 'hover fixture repo\n');
        execSync('git init', { cwd: HOVER_REPO, stdio: 'ignore' });
        execSync('git add -A', { cwd: HOVER_REPO, stdio: 'ignore' });
        execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "init"', {
            cwd: HOVER_REPO,
            stdio: 'ignore',
        });

        const { context, page } = await setupPage(browser);
        try {
            const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: HOVER_REPO } });
            if (!res.ok) throw new Error(`create hover project: ${JSON.stringify(res.data)}`);
            projectId = res.data.id;
        } finally {
            await context.close();
        }
    });

    test.afterAll(async ({ browser }) => {
        const { context, page } = await setupPage(browser);
        try {
            if (projectId) await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
        } finally {
            await context.close();
        }
        rmSync(HOVER_REPO, { recursive: true, force: true });
    });

    /** Open the fixture project's explorer with caller.go in the editor. */
    async function openCaller(page: Page) {
        await armFakeLSP(page);
        await page.goto(`/?project=${projectId}&view=explorer`);
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
        await page.locator('.file-browser-item', { hasText: CALLER }).first().click();
        await expect(page.locator('.cm-content')).toBeVisible({ timeout: 20000 });
        await waitForLSPReady(page);
    }

    test('the hover card survives a slow move to its button, and the click jumps', async ({ page }) => {
        await openCaller(page);

        const hover = await coordsOf(page, 'Alpha()');
        await page.mouse.move(hover.x, hover.y);
        await expect(page.locator(CARD)).toBeVisible({ timeout: 10000 });
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        const button = page.locator(CARD_BUTTON);
        const box = (await button.boundingBox())!;
        // 12 small steps: the pointer has to travel from the symbol to the button.
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 12 });
        await expect(button).toBeVisible();
        await expect(page.locator(CARD)).toBeVisible();

        await button.click();
        await expect(page.locator('.editor-tab-name', { hasText: TARGET }).first()).toBeVisible({ timeout: 15000 });
    });

    test('the card needs its dwell again after the pointer leaves and comes back', async ({ page }) => {
        await openCaller(page);

        const symbol = await coordsOf(page, 'Alpha()');
        await page.mouse.move(symbol.x, symbol.y);
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        // Leaving the identifier ends the card: the pointer no longer asks about `Alpha`.
        const away = (await blankSpot(page))!;
        await page.mouse.move(away.x, away.y, { steps: 3 });
        await expect(page.locator(CARD)).toHaveCount(0);

        // steps: 1 — a multi-step move classifies every point on the way, and crossing a
        // neighbour would re-arm the dwell.
        const arrived = Date.now();
        await page.mouse.move(symbol.x, symbol.y, { steps: 1 });
        await expect(page.locator(CARD)).toBeVisible({ timeout: 5000 });
        const waited = Date.now() - arrived;
        // 300ms rather than 500 as the floor: a slow round trip can only lengthen the wait.
        expect(waited, `card returned after ${waited}ms — the hover dwell was not paid`).toBeGreaterThan(300);
    });

    test('a same-file jump moves the caret instead of opening a tab', async ({ page }) => {
        await openCaller(page);
        const tabsBefore = await page.locator('.editor-tab-name').count();

        const hover = await coordsOf(page, 'Helper()');
        await page.mouse.move(hover.x, hover.y);
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        // Pause on the button before pressing it — the pointer has to travel.
        const box = (await page.locator(CARD_BUTTON).boundingBox())!;
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 10 });
        await page.locator(CARD_BUTTON).click();

        await expect
            .poll(async () =>
                page.evaluate(() => {
                    const el = document.querySelector('.cm-editor') as any;
                    const view = el?.__view;
                    return view ? view.state.doc.lineAt(view.state.selection.main.head).number : -1;
                }),
            )
            .toBe(3);
        expect(await page.locator('.editor-tab-name').count()).toBe(tabsBefore);
    });

    test('Ctrl/Cmd+Click jumps without opening a hover card', async ({ page }) => {
        await openCaller(page);

        const target = await coordsOf(page, 'Alpha()');
        await page.keyboard.down('Control');
        await page.mouse.move(target.x, target.y, { steps: 3 });
        // The editor advertises the gesture while the modifier is held, and no
        // hover card is needed to make the jump.
        await expect(page.locator('.cm-editor.cm-lsp-jump-ready')).toBeVisible();
        await expect(page.locator(CARD)).toHaveCount(0);
        await page.mouse.click(target.x, target.y);
        await page.keyboard.up('Control');

        await expect(page.locator('.editor-tab-name', { hasText: TARGET }).first()).toBeVisible({ timeout: 15000 });
    });

    test('a plain click does not jump and keeps the caret where it was clicked', async ({ page }) => {
        await openCaller(page);
        const spot = await coordsOf(page, 'Helper()');
        await page.mouse.click(spot.x, spot.y);
        await expect(page.locator(CARD)).toHaveCount(0);
        await expect(page.locator('.editor-tab-name', { hasText: TARGET })).toHaveCount(0);
    });

    test('hovering text with no documentation and no definition shows no card', async ({ page }) => {
        await openCaller(page);
        // `package` resolves to nothing in the fake's routing table.
        const spot = await coordsOf(page, 'package');
        await page.mouse.move(spot.x, spot.y);
        await page.waitForTimeout(1600); // measurement window: the 500 ms hover delay plus a round trip to the fake
        await expect(page.locator(CARD)).toHaveCount(0);
    });

    // ── What the card does while it waits ────────────────────────────────────

    test('a request the server never answers ends on a sentence, never on a placeholder', async ({ page }) => {
        await openCaller(page);

        const spot = await coordsOf(page, 'Stalled()');
        await page.mouse.move(spot.x, spot.y);
        await expect(page.locator(CARD)).toBeVisible({ timeout: 10000 });
        const status = page.locator('.cm-lsp-action-status');

        // The placeholder is a moment: the wait gets a name, and the non-answer gets a sentence.
        await expect(status).toContainText('Waiting for fakegopls', { timeout: 5000 });
        // This is the ceiling speaking — after the 8s a request is allowed, inside the 12s wait.
        await expect(status).toHaveText('fakegopls did not answer', { timeout: 12000 });
        await expect(page.locator(CARD)).toBeVisible();

        // Moving away is what tells the server to stop.
        const away = (await blankSpot(page))!;
        await page.mouse.move(away.x, away.y, { steps: 3 });
        await expect
            .poll(
                async () =>
                    page.evaluate(
                        () =>
                            (
                                (window as any).__fakeLSP.log as { method?: string }[]
                            ).filter(m => m.method === '$/cancelRequest').length,
                    ),
                { timeout: 5000 },
            )
            .toBeGreaterThan(0);
    });

    test('each half of the answer lands on the card as it arrives', async ({ page }) => {
        await openCaller(page);

        const spot = await coordsOf(page, 'LateDoc()');
        await page.mouse.move(spot.x, spot.y);
        await expect(page.locator(CARD)).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.cm-lsp-action-status')).toContainText('Waiting for fakegopls', {
            timeout: 5000,
        });

        // Documentation lands at 2.5s and the definition at 6s, and the pointer never left.
        await expect(page.locator('.cm-lsp-action-docs')).toContainText('documentation arrived', {
            timeout: 5000,
        });
        await expect(page.locator('.cm-lsp-action-status')).toBeHidden();
        // The definition is answered at 6s, so it is still outstanding. The button's element is
        // built with the card and its row is hidden until a target lands, so this is a check on
        // visibility.
        await expect(page.locator(CARD_BUTTON)).toBeHidden();
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });
    });

    test('long documentation scrolls beneath the pinned button, without its rules', async ({ page }) => {
        await openCaller(page);

        const hover = await coordsOf(page, 'Documented()');
        await page.mouse.move(hover.x, hover.y);
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        const geometry = await page.evaluate(() => {
            const card = document.querySelector('.cm-lsp-action-card') as HTMLElement;
            const body = document.querySelector('.cm-lsp-action-card-body') as HTMLElement;
            const btn = document.querySelector('.cm-lsp-action-button') as HTMLElement;
            const signature = card.querySelector('pre') as HTMLElement | null;
            const paras = Array.from(card.querySelectorAll('p')) as HTMLElement[];
            /** Whitespace between two stacked blocks — a rendered blank line
             *  shows up here as a whole line box, not as a margin. */
            const gap = (from: Element, to: Element) =>
                Math.round((to.getBoundingClientRect().top - from.getBoundingClientRect().bottom) * 10) / 10;
            const cardBox = card.getBoundingClientRect();
            const btnBox = btn.getBoundingClientRect();
            return {
                rules: card.querySelectorAll('hr').length,
                sections: paras.length,
                signatureToBody: signature && paras[0] ? gap(signature, paras[0]) : null,
                paragraphGap: paras.length > 1 ? gap(paras[0], paras[1]) : null,
                cardOverflowY: getComputedStyle(card).overflowY,
                bodyOverflowY: getComputedStyle(body).overflowY,
                cardScrolled: card.scrollTop,
                cardHeight: cardBox.height,
                bodyOverflows: body.scrollHeight - body.clientHeight,
                buttonInsideCard: btnBox.top >= cardBox.top && btnBox.bottom <= cardBox.bottom + 0.5,
            };
        });

        // The fixture's documentation carries three `---` dividers; none may be rendered …
        expect(geometry.rules).toBe(0);
        // … and the prose they separated must survive.
        expect(geometry.sections).toBeGreaterThan(4);

        // `white-space: pre-wrap` would render the newline the markdown writer puts between
        // block tags as a blank line at every break.
        expect(geometry.signatureToBody).toBeLessThanOrEqual(4);
        expect(geometry.paragraphGap).toBeLessThanOrEqual(4);

        // The documentation scrolls …
        expect(geometry.bodyOverflows).toBeGreaterThan(0);
        expect(geometry.bodyOverflowY).toBe('auto');
        // … the card never does, or the button leaves with it.
        expect(geometry.cardOverflowY).toBe('hidden');
        expect(geometry.cardScrolled).toBe(0);
        // 240px cap plus its own padding and border.
        expect(geometry.cardHeight).toBeLessThanOrEqual(260);
        expect(geometry.buttonInsideCard).toBe(true);
    });

    // ── Where a jump lands ───────────────────────────────────────────────────

    test('the project explorer opens a project jump and ignores a task-scoped one', async ({ page }) => {
        await page.goto(`/?project=${projectId}&view=explorer`);
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
        await page.locator('.file-browser-item', { hasText: CALLER }).first().click();
        await expect(page.locator('.cm-content')).toBeVisible({ timeout: 20000 });
        const tabsBefore = await page.locator('.editor-tab-name').count();

        // A task-scoped jump must not be opened from the project's checkout:
        // that is HEAD, not the task's working tree.
        await page.evaluate(
            ({ path, taskId }) =>
                window.dispatchEvent(new CustomEvent('open-file', { detail: { path, line: 3, taskId } })),
            { path: join(HOVER_REPO, TARGET), taskId: 'not-the-open-task' },
        );
        // The open-file handler mounts a tab on the event itself, so a paint is the whole window it gets.
        await waitForReaction(page);
        expect(await page.locator('.editor-tab-name').count(), 'task-scoped jump opened a project tab').toBe(tabsBefore);

        // The same event without a task scope is the project's to open.
        await page.evaluate(
            (path) => window.dispatchEvent(new CustomEvent('open-file', { detail: { path, line: 3 } })),
            join(HOVER_REPO, TARGET),
        );
        await expect(page.locator('.editor-tab-name', { hasText: TARGET }).first()).toBeVisible({ timeout: 15000 });
    });

    test('a task-scoped jump issued from the task view opens the task explorer', async ({ page }) => {
        const taskId = await createTaskViaApi(page, projectId, `hover-routing-${Date.now()}`);
        const config = await apiGetConfig(page);
        const worktreeFile = `${config.workspace_dir}/${projectId}/${taskId}/${CALLER}`;

        // The worktree's copy must differ from the committed one, or the assertions below
        // would pass for the project's HEAD copy.
        const init = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/init`, { method: 'POST' });
        expect(init.ok, `init task worktree: ${JSON.stringify(init.data)}`).toBe(true);
        await expect.poll(() => existsSync(worktreeFile), { timeout: 20000 }).toBe(true);
        appendFileSync(worktreeFile, '\n// WORKTREE-ONLY\n');

        // The task view and the explorers share one slot, so while the task view
        // is up there is no listener for the jump at all.
        await page.goto(`/?project=${projectId}&task=${taskId}`);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 20000 });
        await expect(page.locator('.explorer-view')).toHaveCount(0);

        await page.evaluate(
            ({ path, line, task }) =>
                window.dispatchEvent(new CustomEvent('open-file', { detail: { path, line, taskId: task } })),
            { path: worktreeFile, line: 6, task: taskId },
        );

        // Routed to the task's explorer (the worktree's file), not the project's.
        await expect(page).toHaveURL(/view=task-explorer/, { timeout: 20000 });
        await expect(page.locator('.editor-tab-name', { hasText: CALLER }).first()).toBeVisible({ timeout: 30000 });
        // The buffer is the worktree's copy…
        await expect
            .poll(
                async () =>
                    page.evaluate(() => {
                        const el = document.querySelector('.cm-editor') as any;
                        const view = el?.__view;
                        return view && view.state.doc.toString().includes('WORKTREE-ONLY') ? 'worktree' : 'head';
                    }),
                { timeout: 30000 },
            )
            .toBe('worktree');
        // ...and the caret went to the requested line.
        await expect
            .poll(
                async () =>
                    page.evaluate(() => {
                        const el = document.querySelector('.cm-editor') as any;
                        return el?.__view ? el.__view.state.doc.lineAt(el.__view.state.selection.main.head).number : -1;
                    }),
                { timeout: 30000 },
            )
            .toBe(6);
    });
});
