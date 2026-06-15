import { test, expect } from '@playwright/test';
import { createTaskViaApi } from './helpers';
import { execSync } from 'node:child_process';
import fs from 'node:fs';

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';

/** Get the project ID for the git test repo (not the non-git project). */
async function getGitProjectId(page: any): Promise<string> {
  return await page.evaluate(async (testRepo) => {
    const res = await fetch('/api/projects');
    const projects = await res.json();
    // Find the project pointing to the git test repo
    const gitProject = projects.find((p: any) => p.path === testRepo);
    return gitProject?.id || projects[0]?.id || '';
  }, TEST_REPO);
}

test.describe.serial('Merge to origin branch', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should display origin branch in project header after task start', async ({ page }) => {
    test.setTimeout(30000);
    const projectId = await getGitProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `merge-branch-display-${Date.now()}`);

    // Start the task to trigger worktree creation (which captures origin branch)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait a moment for worktree creation
    await page.waitForTimeout(1000);

    // Verify origin_branch is set via API
    const projectData = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects`);
      const projects = await res.json();
      return projects.find((p: any) => p.id === pid);
    }, projectId);

    expect(projectData.origin_branch).toBeTruthy();
    expect(projectData.origin_branch).toMatch(/master|main/);
  });

  test('should merge worktree changes into origin branch on commit-and-done', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getGitProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `merge-commit-${Date.now()}`);

    // Start the task to trigger worktree creation
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for worktree to be created
    await page.waitForTimeout(1000);

    // Get the task to find its branch
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    const taskBranch = taskData.branch;
    expect(taskBranch).toBeTruthy();

    // Make a change directly in the worktree directory (simulate LLM changes)
    const worktreePath = `${TEST_WORKTREES}/${taskId}`;
    if (fs.existsSync(worktreePath)) {
      fs.writeFileSync(`${worktreePath}/test-merge-file.txt`, 'This file was created by the task\n');
      // Stage and commit in the worktree (simulate auto-commit)
      try {
        execSync('git add -A', { cwd: worktreePath });
        execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Task changes" --allow-empty', { cwd: worktreePath });
      } catch (e) {
        // Commit might fail if no changes, that's ok
      }
    }

    // Get origin branch
    const projectData = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects`);
      const projects = await res.json();
      return projects.find((p: any) => p.id === pid);
    }, projectId);
    const originBranch = projectData.origin_branch;
    expect(originBranch).toBeTruthy();

    // Record commit count on origin branch before merge
    const logBefore = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const commitsBefore = logBefore ? logBefore.split('\n').length : 0;

    // Move task to review
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    // Open the task and click "Commit and move to done"
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).toBeVisible({ timeout: 5000 });
    await commitButton.click();

    // Wait for task to be moved to done
    await expect(commitButton).not.toBeVisible({ timeout: 15000 });

    // Verify task status is done
    const updatedTask = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(updatedTask.status).toBe('done');

    // Verify merge commit exists on origin branch
    const logAfter = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const commitsAfter = logAfter ? logAfter.split('\n').length : 0;
    expect(commitsAfter).toBeGreaterThan(commitsBefore);

    // Verify the file change is on the origin branch
    const fileContent = execSync(`git -C ${TEST_REPO} show ${originBranch}:test-merge-file.txt`).toString().trim();
    expect(fileContent).toContain('This file was created by the task');
  });
});
