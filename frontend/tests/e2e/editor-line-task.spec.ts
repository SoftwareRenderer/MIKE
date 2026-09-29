import { test, expect, type Browser, type Page } from '@playwright/test';
import { writeFileSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, createTaskViaApi, navigateToTask, readStored, waitForStored, waitForStable } from './helpers';

// Gutter line selection in the Explorer code editor: a click opens the Create
// Task modal as an INTERACTIVE FLOATING WINDOW (no dim, app behind stays
// usable) with the selected lines prefilled below two leading newlines — the
// caret starts at the top, above the reference, so the description is typed
// first. Multi-line ranges
// come from the editor's own text selection — clicking a line number inside a
// multi-line highlighted range selects the whole range (gutter drag and
// shift+click are gone). Hovering a line number replaces it with a centered yellow
// chat-bubble icon (with a "Create a task from selected lines" tooltip).
// Hovering a gutter line inside a multi-line editor text selection
// live-previews the range (before any click): the inner lines (exclusive of
// the endpoints) show a centered vertical connector instead of their
// numbers, the hovered line shows the chat bubble. Clicking confirms the
// range — the connector stays on the confirmed selection. A single Create button (plus the auto-start toggle,
// default on) creates the task and opens a FLOATING TASK VIEW (Chat only,
// mobile-style, fixed bottom-right above the editor footer, with Close / Delete
// / Open task buttons) — no navigation, explorer stays behind. "Open task"
// navigates to the full task view; browser back restores the explorer with
// the last-viewed file and its scroll position.
//
// Isolation: dedicated repo + project (/tmp/kanban-test-repo-line-task) —
// never the shared kanban-test-repo, so file creation and task worktrees
// cannot race with other specs' git operations in parallel workers.

const LT_REPO = '/tmp/kanban-test-repo-line-task';
const PROJECT_NAME = 'kanban-test-repo-line-task';
const LINES_FILE = 'lines.txt';
// Tall file for the scroll-restoration test (must be scrollable).
const LONG_FILE = 'long.txt';
const LONG_LINES = Array.from({ length: 400 }, (_, i) => `long line ${i + 1}`);

const LINES = [
  'alpha one',
  'bravo two',
  'charlie three',
  'delta four',
  'echo five',
  'foxtrot six',
  'golf seven',
  'hotel eight',
];

/** Prefilled prompt for a gutter selection: two newlines first, then the
 *  reference block (caret starts at the top, above the reference). */
function refFor(start: number, end = start): string {
  let s = `lines.txt:${start === end ? start : `${start}-${end}`}`;
  for (let i = start; i <= end; i++) s += `\n${i}: ${LINES[i - 1]}`;
  return '\n\n' + s;
}

/** page fixtures aren't available in beforeAll/afterAll — create a dedicated context. */
async function setupPage(browser: Browser) {
  const context = await browser.newContext({
    storageState: '/tmp/kanban-test-storage-state.json',
  });
  const page = await context.newPage();
  return { context, page };
}

/** Find a task by a unique substring of its prompt (server truth). */
async function findTaskByPrompt(
  page: Page,
  projectId: string,
  needle: string,
): Promise<{ id: string; status: string; parent_task_id?: string } | null> {
  return await page.evaluate(async ({ pid, n }) => {
    const res = await fetch(`/api/projects/${pid}/board`);
    const board = await res.json();
    const t = (board.tasks || []).find((task: any) => (task.prompt || '').includes(n));
    return t ? { id: t.id, status: t.status, parent_task_id: t.parent_task_id ?? null } : null;
  }, { pid: projectId, n: needle });
}

test.describe('Editor gutter line selection → Create Task', () => {
  test.describe.configure({ mode: 'serial' });
  let projectId = '';

  test.beforeAll(async ({ browser }) => {
    rmSync(LT_REPO, { recursive: true, force: true });
    mkdirSync(LT_REPO, { recursive: true });
    writeFileSync(join(LT_REPO, LINES_FILE), LINES.join('\n') + '\n');
    writeFileSync(join(LT_REPO, LONG_FILE), LONG_LINES.join('\n') + '\n');
    execSync('git init', { cwd: LT_REPO, stdio: 'ignore' });
    execSync(`git add ${LINES_FILE} ${LONG_FILE}`, { cwd: LT_REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: LT_REPO,
      stdio: 'ignore',
    });

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', {
        method: 'POST',
        body: { path: LT_REPO },
      });
      if (!res.ok) throw new Error(`create line-task project: ${JSON.stringify(res.data)}`);
      projectId = res.data.id;
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) {
        await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
      }
    } finally {
      await context.close();
    }
    rmSync(LT_REPO, { recursive: true, force: true });
  });

  async function openProjectExplorer(page: Page) {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
  }

  /** Wait until the editor's box stops moving (split-pane/ResizeObserver settle).
   *  Gutter clicks are pixel-precise, so a shifting layout would mis-target lines. */
  async function waitEditorStable(page: Page) {
    await waitForStable(async () => {
      const b = await page.locator('.cm-editor').boundingBox();
      return b ? `${Math.round(b.x)}:${Math.round(b.y)}:${Math.round(b.width)}:${Math.round(b.height)}` : '';
    }, { what: 'the editor box', until: (box) => box !== '', timeout: 10_000 });
  }

  async function openLinesFile(page: Page) {
    await page.locator('.file-browser-item', { hasText: LINES_FILE }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.cm-content')).toContainText(LINES[0], { timeout: 10000 });
    // Let the gutter/layout settle before pixel-precise gutter interactions
    await expect(gutterLine(page, 1)).toBeVisible();
    await waitEditorStable(page);
  }

  /** The line-task modal's single Create button. Scoped to the modal dialog by
   *  its accessible name — the ExplorerView is ALSO a role=dialog and its
   *  toolbar has "Create new file/folder" buttons. */
  function createButton(page: Page) {
    return page
      .getByRole('dialog', { name: /Create (Task|Subtask)/ })
      .getByRole('button', { name: /^Create Task/ });
  }

  function gutterLine(page: Page, n: number) {
    // .cm-gutterElement inside the git gutter; nth(n-1) = document line n
    return page.locator('.cm-gutter.cm-gitGutter .cm-gutterElement').nth(n - 1);
  }

  async function clickGutterLine(page: Page, n: number) {
    const box = await gutterLine(page, n).boundingBox();
    expect(box).toBeTruthy();
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  }

  /** Select editor TEXT spanning lines `from`..`to` — a normal multi-line
   *  editor selection, the source of gutter multi-line selections. */
  async function dragEditorText(page: Page, from: number, to: number) {
    const line = (n: number) => page.locator('.cm-content .cm-line').nth(n - 1);
    const a = await line(from).boundingBox();
    const b = await line(to).boundingBox();
    expect(a && b).toBeTruthy();
    // Drag from the start of line `from` to the start of line `to`.
    await page.mouse.move(a!.x + 4, a!.y + a!.height / 2);
    await page.mouse.down();
    // Step through so mousemove events extend the selection progressively.
    const steps = 6;
    for (let i = 1; i <= steps; i++) {
      await page.mouse.move(b!.x + 4, a!.y + ((b!.y - a!.y) * i) / steps);
    }
    await page.mouse.up();
  }

  /** Assert the gutter hover affordance on line n: number hidden, bubble shown,
   *  and the native "create task" tooltip present. */
  async function expectGutterHoverBubble(page: Page, n: number) {
    const cell = gutterLine(page, n);
    await cell.hover();
    const hover = await cell.evaluate((el) => ({
      numHidden: (() => {
        const num = el.querySelector('[data-line] > span:last-child');
        return num ? getComputedStyle(num).visibility === 'hidden' : false;
      })(),
      bubble: getComputedStyle(el, '::after').backgroundImage.includes('svg'),
      title: el.querySelector('[data-line]')?.getAttribute('title') ?? null,
    }));
    expect(hover).toEqual({ numHidden: true, bubble: true, title: 'Create a task from selected lines' });
  }

  /** Gutter row states for lines n: number hidden? connector shown? hover bubble? */
  async function gutterStates(page: Page, lines: number[]) {
    return page.evaluate((ns) => {
      const rows = [...document.querySelectorAll('.cm-gutter.cm-gitGutter .cm-gutterElement')];
      return ns.map((n) => {
        const el = rows[n - 1];
        const num = el.querySelector('[data-line] > span:last-child');
        return {
          numHidden: num ? getComputedStyle(num).visibility === 'hidden' : null,
          connector: getComputedStyle(el, '::before').content !== 'none',
          bubble: getComputedStyle(el, '::after').backgroundImage.includes('svg'),
        };
      });
    }, lines);
  }

  async function expectLineTaskModal(page: Page, prompt: string) {
    await expect(page.locator('#modal-title')).toBeVisible({ timeout: 10000 });
    const textarea = page.locator('#task-description');
    await expect(textarea).toBeVisible();
    await expect(textarea).toHaveValue(prompt, { timeout: 5000 });
  }

  /** The line-task modal is a floating window: transparent, click-through overlay. */
  async function expectFloatingModalOverlay(page: Page) {
    const overlay = page.locator('.overlay.overlay-floating');
    await expect(overlay).toBeVisible();
    const style = await overlay.evaluate((el) => {
      const cs = getComputedStyle(el);
      return { bg: cs.backgroundColor, pe: cs.pointerEvents };
    });
    expect(['transparent', 'rgba(0, 0, 0, 0)']).toContain(style.bg);
    expect(style.pe).toBe('none');
  }

  async function dismissTaskModal(page: Page) {
    // The floating modal only owns Escape while focus is inside it — a click
    // in the background moves focus, so refocus the modal first.
    await page.locator('#task-description').click();
    // Esc → "Discard task?" confirm (the prefilled prompt is non-empty)
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('#modal-title')).toBeHidden({ timeout: 5000 });
  }

  test('clicking a line number opens the modal prefilled with that line', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    // The gutter is a control — pointer cursor
    await expect(gutterLine(page, 1)).toHaveCSS('cursor', 'pointer');
    // Hover affordance: the number is replaced by the yellow chat bubble
    await expectGutterHoverBubble(page, 1);
    await clickGutterLine(page, 1);
    await expectLineTaskModal(page, refFor(1));
    await expect(page.locator('#modal-title')).toHaveText('Create Task');
    // Caret starts at the top — the description is typed above the reference
    expect(await page.locator('#task-description').evaluate((el) => el.selectionStart)).toBe(0);
    // Floating window: no dim behind the modal
    await expectFloatingModalOverlay(page);
    // Single Create button in this variant (key-hint suffix in the accessible name)
    await expect(createButton(page)).toBeVisible();
    await expect(page.getByRole('button', { name: /Create in background/ })).toBeHidden();
    await expect(page.getByRole('button', { name: /Create and Open Task/ })).toBeHidden();
    // Auto start is on by default
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    await dismissTaskModal(page);
  });

  test('clicking a line inside the editor text selection prefills the whole range', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    // Highlight lines 5–6 with a normal editor text selection
    await dragEditorText(page, 5, 6);
    // Click a line number INSIDE the highlighted range (line 6, the last one)
    await clickGutterLine(page, 6);
    await expectLineTaskModal(page, refFor(5, 6));
    await dismissTaskModal(page);
  });

  test('hovering a gutter line inside a text selection previews the range with a centered connector', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    // Highlight lines 4–7 in the editor, then HOVER the middle line number (5) — no click
    await dragEditorText(page, 4, 7);
    const cell = gutterLine(page, 5);
    await cell.hover();
    // The connector must only render in the git gutter — gutterLineClass
    // applies the class in every gutter, so the CSS is scoped to cm-gitGutter
    // (regression: a duplicate line appeared in the fold gutter).
    const connectorInOtherGutter = await page.evaluate(() => {
      for (const g of document.querySelectorAll('.cm-gutter')) {
        if (g.classList.contains('cm-gitGutter')) continue;
        for (const el of g.querySelectorAll<HTMLElement>('.cm-gutterElement')) {
          if (getComputedStyle(el, '::before').content !== 'none') return true;
        }
      }
      return false;
    });
    expect(connectorInOtherGutter).toBe(false);
    // numbers; hovered line 5 shows the chat bubble (its connector yields);
    // inner line 6 shows the centered vertical connector.
    expect(await gutterStates(page, [4, 5, 6, 7])).toEqual([
      { numHidden: false, connector: false, bubble: false },
      { numHidden: true, connector: false, bubble: true },
      { numHidden: true, connector: true, bubble: false },
      { numHidden: false, connector: false, bubble: false },
    ]);
    // Moving the mouse away clears the preview
    await page.mouse.move(5, 5);
    expect(await gutterStates(page, [4, 5, 6, 7])).toEqual([
      { numHidden: false, connector: false, bubble: false },
      { numHidden: false, connector: false, bubble: false },
      { numHidden: false, connector: false, bubble: false },
      { numHidden: false, connector: false, bubble: false },
    ]);
    // Clicking confirms the range — the modal opens with the full 4–7
    // reference and the confirmed selection keeps the connector behind it
    await clickGutterLine(page, 5);
    await expectLineTaskModal(page, refFor(4, 7));
    await page.mouse.move(5, 5); // park the mouse so no hover state interferes
    expect(await gutterStates(page, [4, 5, 6, 7])).toEqual([
      { numHidden: false, connector: false, bubble: false },
      { numHidden: true, connector: true, bubble: false },
      { numHidden: true, connector: true, bubble: false },
      { numHidden: false, connector: false, bubble: false },
    ]);
    await dismissTaskModal(page);
  });

  test('clicking a line outside the text selection selects only that line', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    // Highlight lines 1–2 in the editor
    await dragEditorText(page, 1, 2);
    // Click a gutter line OUTSIDE the highlighted range
    await clickGutterLine(page, 4);
    await expectLineTaskModal(page, refFor(4));
    await dismissTaskModal(page);
  });

  test('the app behind the floating modal stays interactive', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 1);
    await expectLineTaskModal(page, refFor(1));
    // Click the editor content that is still visible (right of the centered
    // modal) — the cursor moves in the background editor while the modal
    // stays open with its prefill untouched.
    const modalBox = await page.locator('.modal').boundingBox();
    const editorBox = await page.locator('.cm-content').boundingBox();
    const lineBox = await gutterLine(page, 4).boundingBox();
    const clickX = modalBox.x + modalBox.width + 40;
    expect(clickX).toBeLessThan(editorBox.x + editorBox.width);
    const activeLine = () =>
      page.evaluate(() =>
        [...document.querySelectorAll('.cm-gutter.cm-gitGutter .cm-gutterElement')].findIndex((el) =>
          el.classList.contains('cm-activeLineGutter'),
        ),
      );
    expect(await activeLine()).toBe(0); // cursor still on line 1
    await page.mouse.click(clickX, lineBox.y + lineBox.height / 2);
    await expect.poll(activeLine, { timeout: 5000 }).toBe(3); // cursor moved to line 4
    // Modal still open, prefill untouched
    await expect(page.locator('#task-description')).toHaveValue(refFor(1));
    await dismissTaskModal(page);
  });

  test('Create with auto-start opens the floating task view and starts the task', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 2);
    await expectLineTaskModal(page, refFor(2));
    await createButton(page).click();
    // Modal closed; NO navigation — explorer still visible, URL unchanged
    await expect(page.locator('#modal-title')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.explorer-view')).toBeVisible();
    await expect(page).not.toHaveURL(/task=/);
    // Floating task view (Chat only) over the explorer
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    // Chat only: no tab bar, diff column hidden
    await expect(floating.locator('.mobile-tab-bar')).toHaveCount(0);
    await expect(floating.locator('.column-chat')).toBeVisible();
    await expect(floating.locator('.column-diff')).toBeHidden();
    // Task left the backlog (auto-started); the mock LLM keeps it running
    await expect.poll(async () => {
      const t = await findTaskByPrompt(page, projectId, 'lines.txt:2');
      return t?.status ?? 'missing';
    }, { timeout: 20000 }).not.toBe('backlog');
  });

  test('the floating chat streams task output in real time', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 7);
    await expectLineTaskModal(page, refFor(7));
    await createButton(page).click();
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    // The task was just created, so its chat history is empty — anything in
    // the floating chat can only come from LIVE WS events. The mock routes
    // this prompt to the plan flow (the plan-first skill is in every system
    // prompt) → first LLM call is a create_task tool call; that tool-call
    // entry must appear in the FLOATING chat. The floating task is not
    // appStore.selectedTask, so its events used to be dropped by App's
    // onTaskEvent gate and the chat stayed frozen.
    // first(), not a bare locator: the routed flow can emit several tool calls, and the
    // assertion is about liveness, not about their number.
    await expect(floating.locator('.tool-call-entry').first()).toBeVisible({ timeout: 30000 });
  });

  test('the floating view is chat-only with Close, Delete and Open task buttons', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 3);
    await expectLineTaskModal(page, refFor(3));
    await createButton(page).click();
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    await expect(floating.locator('.column-context')).toBeHidden();
    // Chat fills the window (its width is a 3-column share — without the
    // floating stretch rule the right part of the window sits blank)
    const pageBox = await floating.boundingBox();
    const chatBox = await floating.locator('.column-chat').boundingBox();
    expect(pageBox && chatBox, 'floating page/chat boxes').toBeTruthy();
    expect(Math.abs(chatBox!.width - pageBox!.width)).toBeLessThanOrEqual(4);
    // Header buttons: exactly Close (X), Delete task, Open task
    const header = floating.locator('.header');
    await expect(header.getByRole('button', { name: 'Close (Esc)' })).toBeVisible();
    await expect(header.getByRole('button', { name: 'Delete task' })).toBeVisible();
    await expect(header.getByRole('button', { name: 'Open task' })).toBeVisible();
    await expect(header.getByRole('button', { name: 'Open terminal' })).toHaveCount(0);
    await expect(header.getByRole('button', { name: 'Open explorer' })).toHaveCount(0);
    // Close (X) — explorer stays open with the file
    await floating.locator('.btn-back').click();
    await expect(floating).toBeHidden({ timeout: 5000 });
    await expect(page.locator('.explorer-view')).toBeVisible();
  });

  test('Open task navigates to the full task view', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 5);
    await expectLineTaskModal(page, refFor(5));
    // Keep auto-start off — deterministic, no LLM run
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await createButton(page).click();
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    await floating.getByRole('button', { name: 'Open task' }).click();
    // The full task view replaces the floating window; the URL gains task=
    await expect(floating).toBeHidden({ timeout: 5000 });
    await expect(page.locator('.task-detail-page:not(.floating)')).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(/task=/);
    // Back to the board again (header back button)
    await page.locator('.task-detail-page .btn-back').click();
    await expect(page.locator('.task-detail-page')).toBeHidden({ timeout: 5000 });
  });

  test('browser back restores the explorer with the last file and scroll', async ({ page }) => {
    await openProjectExplorer(page);
    // Open the tall file and scroll to ~middle
    await page.locator('.file-browser-item', { hasText: LONG_FILE }).first().click();
    await page.waitForSelector('.explorer-view .cm-editor', { timeout: 10000 });
    await waitEditorStable(page);
    const savedTop = await page.evaluate(() => {
      const scroller = document.querySelector('.explorer-view .file-editor.active .cm-scroller');
      scroller!.scrollTop = 2000;
      return scroller!.scrollTop;
    });
    // The restore below can only restore what was saved: wait for the throttled editor→tab pipeline to
    // have written this scroll offset into the project's stored tab state.
    await waitForStored<any>(page, 'editor-tabs', projectId,
      (state) => Object.values(state?.scrollTops ?? {}).some((v: any) => Number(v) >= 1900));
    // Create a task from a line in the MIDDLE of the viewport. The gutter is
    // virtualized (rendered buffer extends past the viewport), so pick the
    // line at the viewport center via the CM6 view — clicking an off-screen
    // buffered line would make Playwright scroll the editor first.
    const lineNo = await page.evaluate(() => {
      // __view is exposed on the .cm-editor root (FileEditor createEditor)
      const v = (document.querySelector('.explorer-view .file-editor.active .cm-editor') as any)?.__view;
      const pos = v.posAtCoords({ x: 10, y: v.scrollDOM.clientHeight / 2 });
      return v.state.doc.lineAt(pos).number;
    });
    await page.locator(`.cm-gutter.cm-gitGutter [data-line="${lineNo}"]`).click();
    await expectLineTaskModal(page, `\n\nlong.txt:${lineNo}\n${lineNo}: long line ${lineNo}`);
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await toggle.click();
    await createButton(page).click();
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    await floating.getByRole('button', { name: 'Open task' }).click();
    await expect(page.locator('.task-detail-page:not(.floating)')).toBeVisible({ timeout: 10000 });
    // Task-explorer hop: the task explorer shares the project's editor-tab
    // storage key — mounting it must NOT wipe the project explorer's persisted
    // scroll state (regression: it saved the tab state without the scroll
    // fields). Its tab save flushes on mount.
    await page.locator('.task-detail-page:not(.floating) .header')
      .getByRole('button', { name: 'Open explorer' }).click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    // The task explorer's mount flushes its own tab save; wait for that write to settle, then check the
    // project's stored scroll fields are the ones still on disk.
    await waitForStable(() => readStored(page, 'editor-tabs', projectId),
      { what: 'the persisted tab state after the mount', timeout: 10_000 });
    await expect.poll(() => readStored<any>(page, 'editor-tabs', projectId).then(s => Object.keys(s?.scrollTops ?? {}).length))
      .toBeGreaterThan(0);
    // Browser back → full task view → the previous URL entry
    // (view=explorer&file=long.txt)
    await page.goBack();
    await expect(page.locator('.task-detail-page:not(.floating)')).toBeVisible({ timeout: 10000 });
    await page.goBack();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    // long.txt is the active file again
    await expect(page.locator('.explorer-view .cm-content')).toContainText(`long line ${lineNo}`, { timeout: 10000 });
    // Editor scroll restored near the saved offset
    await expect.poll(async () => {
      const top = await page.evaluate(() =>
        document.querySelector('.explorer-view .file-editor.active .cm-scroller')?.scrollTop ?? -1);
      return Math.abs(top - savedTop);
    }, { timeout: 15000 }).toBeLessThanOrEqual(40);
  });

  test('delete from the floating view removes the task', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 6);
    await expectLineTaskModal(page, refFor(6));
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await toggle.click();
    await createButton(page).click();
    const floating = page.locator('.task-detail-page.floating');
    await expect(floating).toBeVisible({ timeout: 10000 });
    await floating.getByRole('button', { name: 'Delete task' }).click();
    // Confirm dialog (shared with the full task view's delete)
    await page.locator('.btn-confirm').click();
    await expect(floating).toBeHidden({ timeout: 5000 });
    // Task is gone from the board (server truth)
    await expect.poll(async () => await findTaskByPrompt(page, projectId, 'lines.txt:6'), { timeout: 10000 })
      .toBeNull();
  });

  test('auto-start off leaves the task in the backlog (floating view still opens)', async ({ page }) => {
    await openProjectExplorer(page);
    await openLinesFile(page);
    await clickGutterLine(page, 4);
    await expectLineTaskModal(page, refFor(4));
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await createButton(page).click();
    await expect(page.locator('#modal-title')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.task-detail-page.floating')).toBeVisible({ timeout: 10000 });
    // Task exists and stays in the backlog (give any auto-start a chance to (wrongly) fire)
    await expect.poll(async () => {
      const t = await findTaskByPrompt(page, projectId, 'lines.txt:4');
      return t ? t.status : 'missing';
    }, { timeout: 20000 }).toBe('backlog');
    // Close the floating view for the next tests
    await page.locator('.task-detail-page.floating .btn-back').click();
    await expect(page.locator('.task-detail-page.floating')).toBeHidden({ timeout: 5000 });
  });

  test('the default create-task flow is unchanged (single Create Task button, dimmed modal)', async ({ page }) => {
    await openProjectExplorer(page);
    // Open the modal the usual way (C shortcut is board-scoped; close the explorer first)
    await page.locator('.explorer-view .close-btn').click();
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await page.keyboard.press('c');
    await expect(page.locator('#modal-title')).toBeVisible({ timeout: 5000 });
    // Single default button (its key-hint suffix is part of the accessible name)
    await expect(
      page.getByRole('dialog', { name: 'Create Task' }).getByRole('button', { name: /^Create Task/ }),
    ).toBeVisible();
    await expect(page.getByRole('button', { name: /Create in background/ })).toBeHidden();
    await expect(page.getByRole('button', { name: /Create and Open Task/ })).toBeHidden();
    await expect(page.locator('.toggle-switch[aria-label="Auto start after creating"]')).toBeHidden();
    // Default modals are NOT floating — the dimmed overlay stays
    const overlay = page.locator('.overlay');
    const style = await overlay.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(['transparent', 'rgba(0, 0, 0, 0)']).not.toContain(style);
    await page.locator('#task-description').fill('');
    await page.keyboard.press('Escape');
    await expect(page.locator('#modal-title')).toBeHidden({ timeout: 5000 });
  });

  test('task explorer line selection opens the modal as a subtask', async ({ page }) => {
    // Create a parent task via the API and open its task view
    const parentTaskId = await createTaskViaApi(page, projectId, `line-task parent ${Date.now()}`);
    await navigateToTask(page, parentTaskId, projectId);
    // Open the Task Explorer (Task Header button, next to the terminal button)
    await page.locator('[aria-label="Open explorer"]').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    // Open the file from the task worktree (initialized on mount)
    await expect(page.locator('.file-entry', { hasText: LINES_FILE })).toBeVisible({ timeout: 20000 });
    await page.locator('.file-entry', { hasText: LINES_FILE }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.cm-content')).toContainText(LINES[0], { timeout: 10000 });
    // Select the last line (after the split-pane layout settles)
    await waitEditorStable(page);
    await clickGutterLine(page, 8);
    await expectLineTaskModal(page, refFor(8));
    // Subtask variant of the modal
    await expect(page.locator('#modal-title')).toHaveText('Create Subtask');
    await expect(page.locator('.parent-label')).toContainText('Subtask of:');
    // Keep auto-start off so the assertions below are deterministic
    const toggle = page.locator('.toggle-switch[aria-label="Auto start after creating"]');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    // Create → the new task is a subtask of the parent; floating view opens
    await createButton(page).click();
    await expect(page.locator('#modal-title')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.task-detail-page.floating')).toBeVisible({ timeout: 10000 });
    await expect.poll(async () => {
      const t = await findTaskByPrompt(page, projectId, 'lines.txt:8');
      return t ? { parent: t.parent_task_id, status: t.status } : null;
    }, { timeout: 20000 }).toEqual({ parent: parentTaskId, status: 'backlog' });
  });

});