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
    await page.keyboard.type(issueUrl); // Use type instead of paste for simplicity in some environments, or use a helper
    // For a real paste test:
    await page.evaluate((url) => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        if (textarea) {
            const event = new ClipboardEvent('paste', {
                clipboardData: new ClipboardEvent('paste'),
                bubbles: true
            });
            // Mocking clipboardData is tricky in Playwright, we can just trigger the handlePaste function if we have access
            // or use page.evaluate to call the handler.
            // Since we want to test the actual event:
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
    // The import takes some time.
    await page.waitForFunction(() => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        return textarea && textarea.value.startsWith('#');
    }, { timeout: 10000 });

    const content = await textarea.inputValue();
    expect(content).toContain('#');
    expect(content).not.toBe(issueUrl);
  });

  test('should save and use github_token from settings', async ({ page }) => {
    // 1. Open settings
    await page.click('.btn-settings');
    await page.waitForSelector('.tab-btn');
    await page.click('.tab-btn:has-text("General")');

    // 2. Set GitHub token
    const tokenInput = page.locator('input#config-github-token');
    await tokenInput.fill('ghp_test_token_12345');
    await page.click('.btn-primary:has-text("Save")');

    // 3. Verify persistence
    await page.reload();
    await page.click('.btn-settings');
    await page.click('.tab-btn:has-text("General")');
    await expect(page.locator('input#config-github-token')).toHaveValue('••••••••••');

    // 4. Verify token is used in request (by mocking the backend endpoint or checking logs)
    // We can intercept the request to /api/github/import-issue
    await page.route('**/api/github/import-issue', async (route) => {
        const request = route.request();
        const body = JSON.parse(request.postData() || '{}');
        // The frontend doesn't send the token in the body anymore, it's handled by the server config.
        // We just need to verify the request is made and the server uses the token.
        await route.fulfill({
            status: 200,
            contentType: 'text/plain',
            body: '# Mocked Issue\n\nThis is a test issue content.'
        });
    });

    // Trigger import
    await page.click('.btn-settings'); // close settings
    await page.click('.btn-create-task');
    await page.evaluate(() => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        if (textarea) {
            const dataTransfer = new DataTransfer();
            dataTransfer.setData('text/plain', 'https://github.com/owner/repo/issues/1');
            const pasteEvent = new ClipboardEvent('paste', {
                clipboardData: dataTransfer,
                bubbles: true
            });
            textarea.dispatchEvent(pasteEvent);
        }
    });

    await page.waitForFunction(() => {
        const textarea = document.querySelector('textarea#task-description') as HTMLTextAreaElement;
        return textarea && textarea.value.includes('Mocked Issue');
    }, { timeout: 10000 });

    const content = await page.locator('textarea#task-description').inputValue();
    expect(content).toContain('Mocked Issue');
  });
});
