import { test, expect, type Browser, type Page } from '@playwright/test';
import { writeFileSync, appendFileSync, existsSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, apiGetConfig, createTaskViaApi, hasInstalledLspExtension } from './helpers';

/**
 * Regression coverage for the editor's go-to-definition UX.
 *
 * The reported bug: "Go to Definition sometimes disappears when trying to click
 * it." The old affordance was a `hoverTooltip` whose range was a single
 * character, and CodeMirror's hover plugin dismisses a hover the moment the
 * pointer leaves that character (±4px around the tooltip box) — so the motion
 * needed to reach the button was the motion that closed it. A fast pointer
 * survived, a careful one did not.
 *
 * The tests below drive a REAL browser with real layout (jsdom has neither),
 * and move the pointer in small steps — the deliberate slow move that used to
 * lose the card.
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
const PROJECT_NAME = 'kanban-test-repo-hover';
const CALLER = 'caller.go';
const TARGET = 'target.go';

// caller.go — `Helper` is defined in this file, `Alpha` in target.go, and
// `Documented` is the symbol the fake gives long, rule-filled documentation.
//   1 package hover
//   2
//   3 func Helper() int { return 2 }
//   4
//   5 func Use() int {
//   6     return Helper() + Alpha()
//   7 }
//   8
//   9 func Documented() int { return 3 }
const CALLER_SRC = `package hover

func Helper() int { return 2 }

func Use() int {
\treturn Helper() + Alpha()
}

func Documented() int { return 3 }
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
     * taller than any reasonable tooltip. This is the input that used to push the
     * card's "Go to Definition" button below the fold.
     */
    const PARAGRAPH =
        'Documents the symbol at enough length that the documentation itself has to scroll inside the card, ' +
        'which is exactly the situation the pinned action row exists for. ';
    const section = (n: number) =>
        Array.from({ length: n }, (_, i) => `Section ${i + 1}. ${PARAGRAPH}${PARAGRAPH}`).join('\n\n');
    const LONG_DOC =
        '```go\nfunc Documented() int\n```\n\n---\n\n' + section(4) + '\n\n---\n\n' + section(4) + '\n\n---\n';

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
                case 'textDocument/definition': {
                    const { line, character } = params.position;
                    const uri = params.textDocument.uri;
                    const word = this.wordAt(uri, line, character);
                    if (word === 'Alpha') {
                        this.reply(msg, [{ targetUri: TARGET_URI, targetRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 10 } }, targetSelectionRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 10 } } }]);
                    } else if (word === 'Helper') {
                        // Same-file definition (line 3, 1-based).
                        this.reply(msg, [{ targetUri: uri, targetRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 11 } }, targetSelectionRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 11 } } }]);
                    } else if (word === 'Documented') {
                        // Same-file, and far enough down that a card which grew past
                        // its own height used to park its button off-screen.
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
            capabilities: { lsp: { language: 'Go', extensions: ['go'], editor_enabled: true, tool_calls_enabled: false } },
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
        // Past the END of the last line's text, not below it. The area under the
        // code is only blank while the fixture ends higher up: a "blank" spot that
        // lands on an identifier turns the pointer move into a re-target, which is
        // a different test than leaving the card alone.
        const at = view.coordsAtPos(view.state.doc.line(view.state.doc.lines).to);
        if (!at) return null;
        return { x: Math.min(rect.right - 8, at.right + 80), y: Math.max(rect.top + 8, at.bottom - 6) };
    });
}

/** Wait until the fake server has seen the document open (LSP is live). */
async function waitForLSPReady(page: Page) {
    await expect
        .poll(async () => page.evaluate(() => ((window as any).__fakeLSP?.log ?? []).some(m => m.method === 'textDocument/didOpen')), { timeout: 20000 })
        .toBe(true);
}

const CARD = '.cm-lsp-action-card';
const CARD_BUTTON = '.cm-lsp-action-button';

// ─── Suite ───────────────────────────────────────────────────────────────────

// serial: the dedicated repo/project is created once for the whole suite.
test.describe.serial('Editor go-to-definition hover + Ctrl/Cmd+Click', () => {
    let projectId = '';

    // The host binary is not the prerequisite — the Go extension has to be installed to answer a hover.
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
        if (!(await hasInstalledLspExtension(page, 'go'))) {
            test.skip(true, 'no installed LSP extension owns .go — install the gopls extension to run LSP specs');
        }
    });

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
        // The regression: 12 small steps from the symbol to the button. The old
        // hover closed on the first of them.
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 12 });
        await expect(button).toBeVisible();
        await expect(page.locator(CARD)).toBeVisible();

        await button.click();
        await expect(page.locator('.editor-tab-name', { hasText: TARGET }).first()).toBeVisible({ timeout: 15000 });
    });

    test('the card waits through a detour and closes once the pointer settles away', async ({ page }) => {
        await openCaller(page);

        const hover = await coordsOf(page, 'Alpha()');
        await page.mouse.move(hover.x, hover.y);
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        // The deliberate reach: leave the identifier, drift away from the card,
        // then come back to the button. A hover that dismisses the moment the
        // pointer leaves the symbol (the old behavior) is gone by the next line.
        const away = (await blankSpot(page))!;
        const button = page.locator(CARD_BUTTON);
        const box = (await button.boundingBox())!;
        // Few, deliberate steps: the trip must stay well inside the grace
        // window, and every Playwright step costs a CDP round trip.
        await page.mouse.move(away.x, away.y, { steps: 3 });
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 3 });
        await expect(button).toBeVisible();

        await button.click();
        await expect(page.locator('.editor-tab-name', { hasText: TARGET }).first()).toBeVisible({ timeout: 15000 });

        // And it does end: parked away from the card, it goes away for good.
        await openCaller(page);
        const symbol = await coordsOf(page, 'Alpha()');
        await page.mouse.move(symbol.x, symbol.y);
        await expect(page.locator(CARD)).toBeVisible({ timeout: 10000 });
        await page.mouse.move(away.x, away.y, { steps: 4 });
        await expect(page.locator(CARD)).toHaveCount(0);
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
        // Comfortably past the 500 ms hover delay plus a round trip to the fake.
        await page.waitForTimeout(1600);
        await expect(page.locator(CARD)).toHaveCount(0);
    });

    test('long documentation scrolls beneath the pinned button, and its rules stay tight', async ({ page }) => {
        await openCaller(page);

        const hover = await coordsOf(page, 'Documented()');
        await page.mouse.move(hover.x, hover.y);
        await expect(page.locator(CARD_BUTTON)).toBeVisible({ timeout: 10000 });

        const geometry = await page.evaluate(() => {
            const card = document.querySelector('.cm-lsp-action-card') as HTMLElement;
            const body = document.querySelector('.cm-lsp-action-card-body') as HTMLElement;
            const btn = document.querySelector('.cm-lsp-action-button') as HTMLElement;
            const rules = Array.from(card.querySelectorAll('hr')) as HTMLElement[];
            const cardBox = card.getBoundingClientRect();
            const btnBox = btn.getBoundingClientRect();
            const firstRule = getComputedStyle(rules[0]);
            const lastRule = getComputedStyle(rules[rules.length - 1]);
            return {
                ruleCount: rules.length,
                ruleMarginTop: firstRule.marginTop,
                ruleMarginBottom: firstRule.marginBottom,
                lastRuleMarginBottom: lastRule.marginBottom,
                cardOverflowY: getComputedStyle(card).overflowY,
                bodyOverflowY: getComputedStyle(body).overflowY,
                cardScrolled: card.scrollTop,
                cardHeight: cardBox.height,
                bodyOverflows: body.scrollHeight - body.clientHeight,
                buttonInsideCard: btnBox.top >= cardBox.top && btnBox.bottom <= cardBox.bottom + 0.5,
            };
        });

        // The fixture's documentation: a signature block, two prose sections split
        // by a rule, and a trailing rule the card must not pad out.
        expect(geometry.ruleCount).toBe(3);
        // The card-scoped rule wins over the generic 6px tooltip margin.
        expect(geometry.ruleMarginTop).toBe('2px');
        expect(geometry.ruleMarginBottom).toBe('2px');
        expect(geometry.lastRuleMarginBottom).toBe('0px');

        // There IS something to scroll, and it is the documentation …
        expect(geometry.bodyOverflows).toBeGreaterThan(0);
        expect(geometry.bodyOverflowY).toBe('auto');
        // … never the card, which is what used to take the button with it.
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
        await page.waitForTimeout(750);
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

        // Make the worktree's copy differ from the committed one. Both trees
        // otherwise hold byte-identical files, and the assertions below would
        // pass for the project's HEAD copy — the exact file a task jump must
        // never open.
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
