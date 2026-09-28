import { test, expect, type Page, type Locator } from '@playwright/test';
import { isolatedProject, ownProjectId, csrfFetch, navigateToTask } from './helpers';

// ============================================================
// CARD TITLE vs ACTION BUTTONS (the Task View's Board column)
// ============================================================
// The buttons are an overlay on the title, so the title must give up exactly the width the card
// draws — the count is decided in the script, the widths in CSS, and these tests are the seam
// between them: the title's clip ends left of the first button, and every drawn button answers
// elementFromPoint at its own centre (the card's own link overlay covers the card body, so a
// button that is merely visible is not reachable). Overflow is layout: jsdom cannot measure it.

const LONG_TITLE = 'Refactor the sandbox bridge connection lifecycle so a reconnecting client cannot observe a half-registered session';

isolatedProject();

/** An in-progress card with an idle agent: the widest reserve a card makes. */
async function idleInProgressTask(page: Page, tag: string): Promise<string> {
    const projectId = ownProjectId();
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { prompt: `title overlap probe ${tag}` },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    const taskId: string = created.data.id;
    const updated = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        body: { title: `${tag}: ${LONG_TITLE}`, status: 'in_progress', execution_state: 'idle' },
    });
    expect(updated.ok, `set title/status: ${JSON.stringify(updated.data)}`).toBe(true);
    return taskId;
}

async function openOnBoard(page: Page, taskId: string) {
    await navigateToTask(page, taskId);
    const boardTab = page.locator('.tab-btn[aria-label="Board overview"]');
    if (await boardTab.isVisible().catch(() => false)) {
        await boardTab.click();
    }
    const card = page.locator(`.column-context .card[data-task-id="${taskId}"]`);
    await expect(card).toBeVisible();
    return card;
}

/** A subtask with a title long enough to be at risk against the row's own action buttons. */
async function longSubtask(page: Page, projectId: string, parentId: string, tag: string, extra: Record<string, unknown> = {}): Promise<string> {
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { prompt: `title overlap probe ${tag}`, parent_task_id: parentId },
    });
    expect(created.ok, `create subtask ${tag}: ${JSON.stringify(created.data)}`).toBe(true);
    const id: string = created.data.id;
    const updated = await csrfFetch(page, `/api/projects/${projectId}/tasks/${id}`, {
        method: 'PUT',
        body: { title: `${tag}: ${LONG_TITLE}`, execution_state: 'idle', ...extra },
    });
    expect(updated.ok, `set subtask title ${tag}: ${JSON.stringify(updated.data)}`).toBe(true);
    return id;
}

function measureCard(page: Page, taskId: string, expectSelected: boolean, expectPainted = false) {
    return page.evaluate(({ id, selected, painted }) => {
        const card = document.querySelector(`.column-context .card[data-task-id="${id}"]`);
        const title = card?.querySelector('.card-title-text');
        const actions = card?.querySelector('.card-actions');
        if (!card || !title || !actions || !actions.children.length) {
            return [`card, title or action buttons missing for ${id}`];
        }
        const box = title.getBoundingClientRect();
        const first = actions.children[0].getBoundingClientRect();
        const clipRight = box.right - (parseFloat(getComputedStyle(title).paddingRight) || 0);
        const problems: string[] = [];
        // A button is faded out by its container, so a hit test on the button itself passes for a row
        // nobody can see: the paint fact is the container's opacity.
        if (painted) {
            const cs = getComputedStyle(actions);
            if (cs.opacity !== '1') problems.push(`its action row renders at opacity ${cs.opacity}`);
        }
        // Otherwise the spec may silently measure a card that was never at risk.
        if (selected && !card.classList.contains('selected')) problems.push('the card under test is not the open one');
        // An untruncated title says nothing about where the clip is.
        if (title.scrollWidth <= title.clientWidth) problems.push(`title was not truncated (${title.scrollWidth} <= ${title.clientWidth})`);
        if (clipRight > first.left) {
            problems.push(`title reserves to ${Math.round(clipRight)}, reaching the first action at ${Math.round(first.left)}`);
        }
        for (const badge of Array.from(actions.querySelectorAll('.action-idle'))) {
            // scrollWidth cannot show this overflow: text-overflow truncates the inline box, so a
            // clipped label reports exactly the slot width. Measure what the label wants.
            const ghost = badge.cloneNode(true) as HTMLElement;
            ghost.style.cssText = 'position:absolute;left:-9999px;width:auto;overflow:visible;white-space:nowrap;display:block';
            document.body.appendChild(ghost);
            const needed = ghost.getBoundingClientRect().width;
            ghost.remove();
            if (needed > badge.clientWidth) {
                problems.push(`Idle label needs ${Math.round(needed)}px and would render clipped in its ${badge.clientWidth}px slot`);
            }
        }
        for (const button of Array.from(actions.querySelectorAll('.action-icon'))) {
            const r = button.getBoundingClientRect();
            if (r.top < 0 || r.bottom > window.innerHeight) continue;
            const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
            if (hit !== button && !button.contains(hit)) {
                const answered = hit ? `${hit.tagName}.${(hit.getAttribute('class') || '').split(' ')[0].replace(/svelte-\w+/, '')}` : 'nothing';
                problems.push(`${button.getAttribute('aria-label')} at ${Math.round(r.left)},${Math.round(r.top)} answered its hit test to ${answered}`);
            }
        }
        return problems;
    }, { id: taskId, selected: expectSelected, painted: expectPainted });
}

test.describe('touch viewport (buttons always shown)', () => {
    test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

    // Everything in this block is the `hover: none` branch. Without the media query matching, the
    // desktop rules would be measured and every assertion below would agree with the wrong device.
    test.beforeEach(async ({ page }) => {
        expect(await page.evaluate(() => window.matchMedia('(hover: none)').matches),
            'this viewport is not a hover-none device').toBe(true);
    });

    test('the open card truncates its title before the action buttons, and its buttons take the tap', async ({ page }) => {
        const taskId = await idleInProgressTask(page, 'open');
        await openOnBoard(page, taskId);

        const problems = await measureCard(page, taskId, true, true);
        expect(problems, 'title and action buttons overlap or are not drawn').toEqual([]);
    });

    test('an expanded subtask card reserves the width of the ⋯ menu it actually draws', async ({ page }) => {
        // On touch a subtask draws one overflow button where the desktop card draws three.
        const projectId = ownProjectId();
        const parentId = await idleInProgressTask(page, 'parent');
        const childId = await longSubtask(page, projectId, parentId, 'child');
        const grandId = await longSubtask(page, projectId, childId, 'grandchild');

        await openOnBoard(page, parentId);
        // The overview expands the open task's children itself, so this must not toggle blindly.
        const subtaskBar = page.locator(`.column-context .card[data-task-id="${parentId}"] .subtask-progress-bar`);
        if ((await subtaskBar.getAttribute('aria-expanded')) !== 'true') {
            await subtaskBar.click();
        }
        const rows = page.locator('.column-context .subtask-wrapper .card');
        await expect(rows).toHaveCount(2);

        // Flat means the depth costs nothing: the grandchild's row starts where the child's does, and
        // it is listed first because the child waits for it.
        const ids = await rows.evaluateAll(els => els.map(el => el.getAttribute('data-task-id')));
        expect(ids).toEqual([grandId, childId]);
        const deep = page.locator(`.column-context .card[data-task-id="${grandId}"] .card-title-text`);
        const shallow = page.locator(`.column-context .card[data-task-id="${childId}"] .card-title-text`);
        expect(Math.abs((await deep.boundingBox())!.x - (await shallow.boundingBox())!.x)).toBeLessThan(1);

        for (const id of ids) {
            // The row draws the one ⋯ menu a touch subtask gets, and draws it visibly.
            await expect(page.locator(`.column-context .card[data-task-id="${id}"] .card-actions`)
                .locator('.action-icon')).toHaveCount(1);
            const problems = await measureCard(page, id, false, true);
            expect(problems, `row ${id} title and action buttons overlap or are not drawn`).toEqual([]);
        }
    });

    test('a card that is not the open one still answers a tap on its own buttons', async ({ page }) => {
        const openId = await idleInProgressTask(page, 'tap');
        await openOnBoard(page, openId);

        const cards = page.locator('.column-context .card');
        const count = await cards.count();
        expect(count).toBeGreaterThan(1);
        let checked = 0;
        for (let i = 0; i < count; i++) {
            const card = cards.nth(i);
            await card.scrollIntoViewIfNeeded();
            const result = await card.evaluate((c) => {
                const header = c.closest('.section-group')?.querySelector('.section-header');
                const headerBottom = header?.getBoundingClientRect().bottom ?? 0;
                const problems: string[] = [];
                let considered = 0;
                for (const button of Array.from(c.querySelectorAll('.action-icon'))) {
                    const r = button.getBoundingClientRect();
                    // Under the sticky header or off-screen, no one could tap it anyway.
                    if (r.top < headerBottom || r.bottom > window.innerHeight) continue;
                    considered++;
                    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
                    if (hit !== button && !button.contains(hit)) {
                        problems.push(`${button.getAttribute('aria-label')} answered its hit test to ${hit?.tagName ?? 'nothing'}`);
                    }
                }
                return { problems, considered };
            });
            checked += result.considered;
            expect(result.problems, `card #${i} buttons are not tappable`).toEqual([]);
        }
        // Otherwise the scroll filter above could skip every button and the loop would pass.
        expect(checked).toBeGreaterThan(2);
    });
});

test.describe('desktop viewport (buttons appear on hover)', () => {
    test.use({ viewport: { width: 1280, height: 800 } });

    test('hovering a card reserves the width without animating the title under the buttons', async ({ page }) => {
        const openId = await idleInProgressTask(page, 'desktop-open');
        const hoverId = await idleInProgressTask(page, 'desktop-hover');
        await openOnBoard(page, openId);

        // The open card is reserved already; only a card the pointer reveals can animate.
        const card = page.locator(`.column-context .card[data-task-id="${hoverId}"]`);
        await card.scrollIntoViewIfNeeded();
        const box = await card.boundingBox();
        expect(box).not.toBeNull();
        await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);

        const animatesPadding = await card.locator('.card-title').evaluate((el) => {
            const cs = getComputedStyle(el);
            const props = cs.transitionProperty.split(',').map(p => p.trim());
            const seconds = (d: string) => (parseFloat(d) || 0) * (d.trim().endsWith('ms') ? 0.001 : 1);
            const durations = cs.transitionDuration.split(',').map(seconds);
            // `transition-property: all` with a zero duration is what "no transition" looks
            // like in computed style, so the duration is the fact and the name is not.
            return props.some((p, i) =>
                (p === 'padding-right' || p === 'padding' || p === 'all')
                && (durations[i] ?? durations[0] ?? 0) > 0);
        });
        expect(animatesPadding,
            'the title reservation animates, so text sits under the buttons while they fade in').toBe(false);

        const problems = await measureCard(page, hoverId, false);
        expect(problems, 'title and action buttons overlap').toEqual([]);
    });

    test('a row gives up the button width only while the buttons are drawn', async ({ page }) => {
        // A row's status word shares its title line, and a status word is unsquashable — `Pulling image`
        // is the longest one a row can be put into from the API — so it is the thing that can cost the
        // title width. (The model badge lives on the row's second line, where it costs the title nothing.)
        const projectId = ownProjectId();
        const parentId = await idleInProgressTask(page, 'reserve');
        const plainId = await longSubtask(page, projectId, parentId, 'reserve-plain');
        const badgeId = await longSubtask(page, projectId, parentId, 'reserve-badge', { execution_state: 'pulling_image' });
        await openOnBoard(page, parentId);

        // The clip is the title box's right edge and the line is what gives the buttons their width, so
        // "the title holds the space" and "the title is clear of the buttons" are the two claims.
        const clip = (row: Locator) => row.evaluate(card => {
            const line = card.querySelector('.card-title')!;
            const title = card.querySelector('.card-title-text')!;
            const actions = card.querySelector('.card-actions')!;
            const badges = card.querySelector('.badges-row.is-inline');
            return {
                clipRight: title.getBoundingClientRect().right,
                firstButtonLeft: actions.children[0].getBoundingClientRect().left,
                titleWidth: title.getBoundingClientRect().width,
                badgeOverflow: badges ? badges.getBoundingClientRect().right - card.getBoundingClientRect().right : 0,
                linePad: getComputedStyle(line).paddingRight,
            };
        });

        for (const [tag, id] of [['plain', plainId], ['long badge', badgeId]] as const) {
            const row = page.locator(`.column-context .card[data-task-id="${id}"]`);
            await row.scrollIntoViewIfNeeded();
            // Both rows carry badges: the status word always, the second one a long `provider · model` too.
            await expect(row.locator('.badges-row.is-inline')).toHaveCount(1);

            const hidden = await clip(row);
            expect(hidden.linePad, `${tag}: an undrawn button row still costs the title line its width`).toBe('0px');
            // A title claims space as surplus only (`flex: 1` over a zero basis), so an unsquashable badge
            // is the shape that leaves it with nothing.
            expect(hidden.titleWidth, `${tag}: the row title was given no width at all`).toBeGreaterThan(0);
            expect(Math.max(0, hidden.badgeOverflow), `${tag}: the row badges hang past the row`).toBeLessThanOrEqual(1);

            await row.hover();
            const shown = await clip(row);
            expect(parseFloat(shown.linePad), `${tag}: hovering drew no reserve`).toBeGreaterThan(0);
            expect(hidden.clipRight - shown.clipRight, `${tag}: revealing the buttons returned the line no width`)
                .toBeGreaterThan(0);
            expect(shown.clipRight, `${tag}: hovering left the title under the buttons`)
                .toBeLessThanOrEqual(shown.firstButtonLeft);

            const problems = await measureCard(page, id, false);
            expect(problems, `${tag}: title and action buttons overlap while the row is hovered`).toEqual([]);
        }
    });

});
