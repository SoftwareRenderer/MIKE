import { test, expect } from '@playwright/test';

test.describe.serial('GitHub Issue Import', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('should import GitHub issue content when URL is pasted', async ({ page }) => {
    // 1. Create a project if none exists
    const projectItems = page.locator('.project-item');
    let project = projectItems.first();
    if (await projectItems.count() === 0) {
      await page.click('.btn-create-project');
      await page.fill('input[placeholder="Project name..."]', 'GitHub Test Project');
      await page.click('.btn-primary');
      project = page.locator('.project-item').first();
    }
    await project.click();

    // 2. Open Create Task modal
    await page.click('.btn-create-task');
    await page.waitForSelector('textarea#task-description');

    // 3. Paste a public GitHub issue URL
    const issueUrl = 'https://github.com/microsoft/typescript/issues/57485';
    const textarea = page.locator('textarea#task-description');
    await textarea.focus();

    await page.evaluate((url) => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        if (textarea) {
            const dataTransfer = new DataTransfer();
            dataTransfer.setData('text/plain', url);
            const pasteEvent = new ClipboardEvent('paste', {
                clipboardData: dataTransfer,
                bubbles: true
            });
            textarea.dispatchEvent(pasteEvent);
        }
    }, issueUrl);

    // 4. Verify description is populated
    await page.waitForFunction(() => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        return textarea && textarea.value.startsWith('#');
    }, { timeout: 10000 });

    const content = await textarea.inputValue();
    expect(content).toContain('#');
    expect(content).not.toBe(issueUrl);
  });
});
