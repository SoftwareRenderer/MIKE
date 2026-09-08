import { test, expect } from '@playwright/test';
import { writeFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { isolatedProject, gotoProject, ownProjectId, ownProjectPath } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

test.describe.configure({ mode: 'serial' });

/** 1-based lines of the two matches, both far below the fold. */
const LINE_FIRST = 120;
const LINE_SECOND = 190;
const TOTAL_LINES = 220;

test.describe('Find in files — revealing a result', () => {
    test('every result click scrolls the open file to its line, including a repeat of one', async ({ page }) => {
        test.setTimeout(60000);
        const projectDir = ownProjectPath();
        if (!projectDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        // Unique per run: the search walks the whole project directory, which other
        // specs on this worker's project have written fixtures into too.
        const query = `revealtarget${Date.now()}`;
        const file = `find-scroll-${query}.txt`;
        const lines = Array.from({ length: TOTAL_LINES }, (_, i) => {
            const n = i + 1;
            return n === LINE_FIRST || n === LINE_SECOND
                ? `const ${query} = ${n}`
                : `// filler ${n} — long enough that the document scrolls`;
        });
        writeFileSync(join(projectDir, file), lines.join('\n') + '\n');

        try {
            await gotoProject(page);
            await page.waitForSelector('.project-item', { timeout: 10000 });
            await page.locator('.explorer-btn').click();
            await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

            await page.keyboard.down('Control');
            await page.keyboard.down('Shift');
            await page.keyboard.press('KeyF');
            await page.keyboard.up('Shift');
            await page.keyboard.up('Control');
            await expect(page.locator('.file-search-panel')).toBeVisible();

            await page.locator('.file-search-panel .search-input').fill(query);

            const resultAt = (line: number) => page.locator('.search-result-item')
                .filter({ has: page.locator('.line-num', { hasText: new RegExp(`^${line}$`) }) });
            await expect(resultAt(LINE_FIRST)).toBeVisible({ timeout: 15000 });
            await expect(resultAt(LINE_SECOND)).toBeVisible();

            const scroller = page.locator('.file-editor.active .cm-scroller');
            const scrollTop = () => scroller.evaluate(el => el.scrollTop);

            // The first click opens the file and reveals its line.
            await resultAt(LINE_FIRST).click();
            await expect.poll(scrollTop, { timeout: 10000 }).toBeGreaterThan(0);
            const atFirst = await scrollTop();

            // The second result is in the SAME file, whose tab is already open. This is
            // the click that did nothing: switching to an open tab never recorded the
            // line, so the editor had no new value to react to.
            await resultAt(LINE_SECOND).click();
            await expect.poll(scrollTop, { timeout: 10000 }).toBeGreaterThan(atFirst);
            const atSecond = await scrollTop();

            // And asking for the first line again is a second request for it, not the
            // same one — it scrolls back even though the number did not change.
            await resultAt(LINE_FIRST).click();
            await expect.poll(scrollTop, { timeout: 10000 }).toBeLessThan(atSecond);
        } finally {
            try { rmSync(join(projectDir, file)); } catch { /* already gone */ }
        }
    });
});
