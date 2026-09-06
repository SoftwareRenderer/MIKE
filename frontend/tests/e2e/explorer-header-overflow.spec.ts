import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import {
    isolatedProject, gotoProject, ownProjectId, ownProjectPath, csrfFetch,
} from './helpers';

// A long branch name used to occupy a 160px badge that could not shrink (`flex-shrink: 0`) beside a
// path that would not shrink either, so the Explorer header overflowed its panel and the
// Open-terminal button was drawn past the right edge — measured at ≤440px with a 50-char branch
// (`types.MaxBranchName`), and at desktop widths once the workspace path was long. Both Explorer
// headers carry that markup, so both are measured here, in a real browser: overflow is layout, and
// jsdom has no layout engine.
//
// The branch text also never got an ellipsis: `text-overflow` only paints on a box whose own
// overflow is hidden, and the badge is a flex container — so its text was an anonymous flex item
// clipped mid-glyph (measured scrollWidth 349 against clientWidth 160 on the badge itself).

const LONG_TITLE = 'Add support for nested workspace scoped dependency caches across providers';
// task/<slug capped at types.BranchSlugMaxLen>-<6 random chars> == types.MaxBranchName (50).
const LONG_BRANCH_PREFIX = 'task/add-support-for-nested-workspace-scope-';
const PROJECT_BRANCH = 'feature/reconcile-dependency-cache-across-workspaces-and-reg'.slice(0, 50);
// Narrowest first: that is where the bug lives, so the failure message names the button that was
// drawn off the panel rather than a later, milder symptom.
const WIDTHS = [320, 420, 480, 768, 1024, 1440];

/** '' when nothing in the header is drawn outside the viewport and the header fits its panel. */
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
        // One line is 43px, two is 57px; a third means the path is no longer clamped.
        const h = header!.getBoundingClientRect().height;
        if (h > 64) problems.push(`header grew to ${Math.round(h)}px tall`);
        return problems.join('; ');
    });
}

test.describe.serial('Explorer header with a long branch name', () => {
    isolatedProject();

    test('task explorer keeps its buttons on screen at every width', async ({ page }) => {
        const projectId = ownProjectId();
        // An explicit title skips title generation (that keys off the prompt), and a one-word prompt
        // keeps the task out of the executor: this spec needs a branch, not an LLM turn.
        const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { title: LONG_TITLE, prompt: 'x', status: 'backlog' },
        });
        expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
        const taskId = created.data.id;

        for (const width of WIDTHS) {
            await page.setViewportSize({ width, height: 800 });
            // The view initializes the worktree (and so the branch) on mount.
            await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
            await expect(page.locator('.explorer-branch-badge'), `${width}px: branch name shown`)
                .toContainText(LONG_BRANCH_PREFIX);
            await expect(() => headerOverflow(page), `${width}px: header fits`).resolves.toBe('');
            await expect(() => badgeProblems(page), `${width}px: badge truncates`).resolves.toBe('');
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
                // view=explorer shows the branch dropdown, view=review the badge variant of it.
                await gotoProject(page, `/?project=${projectId}&view=${view}`);
                const label = page.locator(view === 'review' ? '.branch-badge-label' : '.explorer-name');
                await expect(label, `${width}px/${view}: branch name shown`).toHaveText(PROJECT_BRANCH);
                await expect(() => headerOverflow(page), `${width}px/${view}: header fits`).resolves.toBe('');
                if (view === 'review') {
                    await expect(() => badgeProblems(page), `${width}px/${view}: badge truncates`).resolves.toBe('');
                }
            }
        }
    });
});
