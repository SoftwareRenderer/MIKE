import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import {
    isolatedProject, gotoProject, ownProjectId, ownProjectPath, csrfFetch, descenderProblems,
    getTaskData, waitForTaskState,
} from './helpers';

// Overflow is layout, so it is measured in a real browser: jsdom has no layout engine. Both Explorer headers
// carry the same markup, so both are measured.

// The title route answers with the selected fixture, so this is the one whose text is long enough to fill the
// branch-name budget. The spec reads the branch back from the task row, so the fixture owns the words.
const TITLE_FIXTURE = 'title_long';
// generateBranchName (backend/exec/merge.go) cuts task/<slug>-<random6>, and the slug keeps what is left of
// types.MaxBranchName (50). An ID-named branch is 27 characters, so it is never this.
const MAXIMAL_TASK_BRANCH = /^task\/.{38}-[a-z0-9]{6}$/;
const PROJECT_BRANCH = 'feature/reconcile-dependency-cache-across-workspaces-and-reg'.slice(0, 50);
// Narrowest first, so the first failure is the worst one. 280 is below any phone viewport, and it is included
// because a min-width: 160px branch list stops fitting beside its anchor there.
const WIDTHS = [280, 320, 420, 480, 768, 1024, 1440];

/** headerOverflow names what is drawn outside the viewport or outside its panel, and returns '' when nothing is. */
async function headerOverflow(page: Page): Promise<string> {
    return page.evaluate(() => {
        const header = document.querySelector('.explorer-header');
        if (!header) return 'no .explorer-header';
        const problems: string[] = [];
        for (const b of Array.from(header.querySelectorAll('button'))) {
            const r = b.getBoundingClientRect();
            if (r.width === 0) problems.push('a header button has zero width');
            else if (r.left < 0 || Math.round(r.right) > window.innerWidth) {
                problems.push(`${b.className.split(' ')[0]} drawn at ${Math.round(r.left)}..${Math.round(r.right)} outside 0..${window.innerWidth}`);
            }
        }
        if (header.scrollWidth > header.clientWidth) {
            problems.push(`header overflows its panel: scrollWidth ${header.scrollWidth} > ${header.clientWidth}`);
        }
        return problems.join('; ');
    });
}

/** The badge must truncate its label instead of overflowing, and stay two lines tall at most. */
async function badgeProblems(page: Page): Promise<string> {
    return page.evaluate(() => {
        const header = document.querySelector('.explorer-header');
        const badge = document.querySelector('.explorer-branch-badge');
        const label = badge?.querySelector('.branch-badge-label');
        if (!badge || !label) return 'no .explorer-branch-badge';
        const problems: string[] = [];
        if (badge.scrollWidth > badge.clientWidth) {
            problems.push(`badge content overflows: ${badge.scrollWidth} > ${badge.clientWidth}`);
        }
        if (label.scrollWidth <= label.clientWidth) {
            problems.push(`branch name was not truncated: ${label.scrollWidth} <= ${label.clientWidth}`);
        }
        // One line is 43px and two is 57px. A third line means the path is no longer clamped.
        const h = header!.getBoundingClientRect().height;
        if (h > 64) problems.push(`header grew to ${Math.round(h)}px tall`);
        return problems.join('; ');
    });
}

/**
 * Every header control must be the element a click there hits. headerOverflow cannot see a button that
 * outgrew its wrapper but still leaves the panel's own scroll width alone.
 */
async function coveredControls(page: Page): Promise<string> {
    return page.evaluate(() => {
        const header = document.querySelector('.explorer-header');
        if (!header) return 'no .explorer-header';
        const name = (el: Element) =>
            (typeof el.className === 'string' ? el.className : el.getAttribute('class') || el.tagName).split(' ')[0];
        const problems: string[] = [];
        for (const b of Array.from(header.querySelectorAll('button'))) {
            const r = b.getBoundingClientRect();
            if (r.width === 0 || r.height === 0) {
                problems.push(`${name(b)} has no box to click`);
                continue;
            }
            const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
            if (!top) problems.push(`${name(b)}: nothing at its centre`);
            else if (!b.contains(top)) {
                problems.push(
                    `${name(b)} at ${Math.round(r.x)}..${Math.round(r.right)} is covered by ${name(top)}`
                );
            }
        }
        return problems.join('; ');
    });
}

/** The open branch list stays inside the panel and truncates its own labels instead of being cut. */
async function openListProblems(page: Page): Promise<string> {
    return page.evaluate(() => {
        const view = document.querySelector('.explorer-view');
        const list = document.querySelector('.explorer-dropdown-list');
        if (!view || !list) return 'no open .explorer-dropdown-list';
        const problems: string[] = [];
        const panel = view.getBoundingClientRect();
        const box = list.getBoundingClientRect();
        if (box.left < panel.left || box.right > panel.right) {
            problems.push(
                `branch list spans ${Math.round(box.left)}..${Math.round(box.right)}, outside the panel ` +
                `${Math.round(panel.left)}..${Math.round(panel.right)}`
            );
        }
        for (const item of Array.from(list.querySelectorAll('.explorer-dropdown-item'))) {
            // A row that spills hides the ref it names. An ellipsis inside the label is fine, because the row
            // itself must hold its content.
            if (item.scrollWidth > item.clientWidth) {
                problems.push(
                    `row content overflows its row: ${item.scrollWidth} > ${item.clientWidth}`
                );
            }
        }
        for (const label of Array.from(list.querySelectorAll('.explorer-dropdown-name'))) {
            // An ellipsis must still name the branch: the full ref is reachable on hover.
            if (label.getAttribute('title') !== label.textContent) {
                problems.push('truncated branch name has no title with the full ref');
            }
        }
        return problems.join('; ');
    });
}

test.describe.serial('Explorer header with a long branch name', () => {
    isolatedProject();

    test('task explorer keeps its buttons on screen at every width', async ({ page }) => {
        const projectId = ownProjectId();
        // Only a model-written title buys a long branch. generateBranchName slugs the title only when a model
        // wrote it, and an ID-named branch is too short to overflow, so the task is created titleless with the
        // long-title fixture pinned. Backlog status keeps the executor out of this spec.
        const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { prompt: `reconcile the cache probe with the board llm-fixture:${TITLE_FIXTURE}`, status: 'backlog' },
        });
        expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
        const taskId = created.data.id;

        // A worktree initialized before the title lands keeps the ID-based name for good, so the title is
        // awaited before the view that initializes the worktree is mounted.
        await waitForTaskState(page, projectId, taskId, t => !!t.title && !t.title_generating);
        await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
        await waitForTaskState(page, projectId, taskId, t => !!t.branch);
        const branch = String((await getTaskData(page, projectId, taskId)).branch);
        expect(branch, `branch ${JSON.stringify(branch)} is not a maximal task branch`)
            .toMatch(MAXIMAL_TASK_BRANCH);

        for (const width of WIDTHS) {
            await page.setViewportSize({ width, height: 800 });
            await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
            await expect(page.locator('.explorer-branch-badge'), `${width}px: branch name shown`)
                .toContainText(branch);
            await expect(() => headerOverflow(page), `${width}px: header fits`).resolves.toBe('');
            await expect(() => badgeProblems(page), `${width}px: badge truncates`).resolves.toBe('');
            for (const sel of ['.project-path', '.branch-badge-label']) {
                await expect(() => descenderProblems(page, sel), `${width}px: ${sel} keeps its descenders`)
                    .resolves.toBe('');
            }
        }
    });

    test('project explorer keeps its selector and buttons on screen at every width', async ({ page }) => {
        const projectId = ownProjectId();
        const repo = ownProjectPath();
        expect(repo, 'isolatedProject() did not claim a repo').toBeTruthy();
        execSync(`git -C ${repo} checkout -q -B ${PROJECT_BRANCH}`);

        for (const width of WIDTHS) {
            await page.setViewportSize({ width, height: 800 });
            for (const view of ['explorer', 'review']) {
                // view=explorer shows the branch dropdown, and view=review shows the badge variant of it.
                await gotoProject(page, `/?project=${projectId}&view=${view}`);
                const label = page.locator(view === 'review' ? '.branch-badge-label' : '.explorer-name');
                await expect(label, `${width}px/${view}: branch name shown`).toHaveText(PROJECT_BRANCH);
                await expect(() => headerOverflow(page), `${width}px/${view}: header fits`).resolves.toBe('');
                await expect(() => descenderProblems(page, '.project-path'), `${width}px/${view}: path keeps its descenders`)
                    .resolves.toBe('');
                await expect(() => coveredControls(page), `${width}px/${view}: nothing painted over a control`)
                    .resolves.toBe('');
                if (view === 'review') {
                    await expect(() => badgeProblems(page), `${width}px/${view}: badge truncates`).resolves.toBe('');
                }
            }
        }
    });

    test('no header control is covered by the open branch list', async ({ page }) => {
        const projectId = ownProjectId();
        expect(ownProjectPath(), 'isolatedProject() did not claim a repo').toBeTruthy();
        execSync(`git -C ${ownProjectPath()} checkout -q -B ${PROJECT_BRANCH}`);

        for (const width of WIDTHS) {
            await page.setViewportSize({ width, height: 800 });
            await gotoProject(page, `/?project=${projectId}&view=explorer`);
            await expect(page.locator('.explorer-name'), `${width}px: branch name shown`)
                .toHaveText(PROJECT_BRANCH);
            await page.click('.explorer-dropdown-btn');
            await expect(page.locator('.explorer-dropdown-list')).toBeVisible();
            await expect(() => openListProblems(page), `${width}px: branch list inside the panel`)
                .resolves.toBe('');
            await expect(() => coveredControls(page), `${width}px: controls clickable with the list open`)
                .resolves.toBe('');
            await page.keyboard.press('Escape');
            // Escape closes the branch list, not the panel behind it.
            await expect(page.locator('.explorer-dropdown-list'), `${width}px: Escape closed the list`)
                .toHaveCount(0);
            await expect(page.locator('.explorer-selector'), `${width}px: explorer still open`).toBeVisible();
        }
    });
});
