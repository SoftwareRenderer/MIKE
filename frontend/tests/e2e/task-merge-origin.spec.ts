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

  test('should merge into original branch even after user switches branches', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getGitProjectId(page);

    // Determine the origin branch (main or master)
    const originBranch = execSync(`git -C ${TEST_REPO} rev-parse --abbrev-ref HEAD`).toString().trim();
    expect(originBranch).toMatch(/master|main/);

    // Create a second branch to switch to later
    execSync(`git -C ${TEST_REPO} checkout -b test-develop-${Date.now()}`, { stdio: 'pipe' });
    execSync(`git -C ${TEST_REPO} checkout ${originBranch}`, { stdio: 'pipe' });
    const devBranch = execSync(`git -C ${TEST_REPO} branch --list | grep test-develop`).toString().trim();
    expect(devBranch).toBeTruthy();

    // Create and start task (captures origin branch)
    const taskId = await createTaskViaApi(page, projectId, `merge-branch-switch-${Date.now()}`);

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });
    await page.waitForTimeout(1000);

    // Verify task.base_ref is set to the origin branch
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.base_ref).toBe(originBranch);

    // Make a change in the worktree
    const worktreePath = `${TEST_WORKTREES}/${taskId}`;
    if (fs.existsSync(worktreePath)) {
      fs.writeFileSync(`${worktreePath}/test-branch-switch-file.txt`, 'Branch switch test\n');
      try {
        execSync('git add -A', { cwd: worktreePath });
        execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Branch switch test changes" --allow-empty', { cwd: worktreePath });
      } catch (e) { /* ignore */ }
    }

    // Switch the project repo to the dev branch (simulating user switching branches)
    execSync(`git -C ${TEST_REPO} checkout ${devBranch}`, { stdio: 'pipe' });
    const currentBranch = execSync(`git -C ${TEST_REPO} rev-parse --abbrev-ref HEAD`).toString().trim();
    expect(currentBranch).toBe(devBranch);

    // Record commit counts on both branches before merge
    const originLogBefore = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const originCommitsBefore = originLogBefore ? originLogBefore.split('\n').length : 0;
    const devLogBefore = execSync(`git -C ${TEST_REPO} log --format=%H ${devBranch}`).toString().trim();
    const devCommitsBefore = devLogBefore ? devLogBefore.split('\n').length : 0;

    // Move to review and commit-and-done
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).toBeVisible({ timeout: 5000 });
    await commitButton.click();
    await expect(commitButton).not.toBeVisible({ timeout: 15000 });

    // Verify task is done
    const updatedTask = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(updatedTask.status).toBe('done');

    // Verify merge went into the ORIGINAL branch, NOT the dev branch
    const originLogAfter = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const originCommitsAfter = originLogAfter ? originLogAfter.split('\n').length : 0;
    expect(originCommitsAfter).toBeGreaterThan(originCommitsBefore);

    // The file should exist on the origin branch
    const fileContent = execSync(`git -C ${TEST_REPO} show ${originBranch}:test-branch-switch-file.txt`).toString().trim();
    expect(fileContent).toContain('Branch switch test');

    // Cleanup: switch back to origin branch
    execSync(`git -C ${TEST_REPO} checkout ${originBranch}`, { stdio: 'pipe' });
    execSync(`git -C ${TEST_REPO} branch -D ${devBranch}`, { stdio: 'pipe' });
  });

  test('should keep task branch after commit-and-done, delete it after task deletion', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getGitProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `branch-cleanup-${Date.now()}`);

    // Start the task to trigger worktree creation
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });
    await page.waitForTimeout(1000);

    // Get task branch
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const taskBranch = taskData.branch;
    expect(taskBranch).toBeTruthy();

    // Verify branch exists in repo
    const branchesBefore = execSync(`git -C ${TEST_REPO} branch --list ${taskBranch}`).toString().trim();
    expect(branchesBefore).toContain(taskBranch);

    // Move to review and commit-and-done
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).toBeVisible({ timeout: 5000 });
    await commitButton.click();
    await expect(commitButton).not.toBeVisible({ timeout: 15000 });

    // Branch should STILL exist after commit-and-done
    const branchesAfterCommit = execSync(`git -C ${TEST_REPO} branch --list ${taskBranch}`).toString().trim();
    expect(branchesAfterCommit).toContain(taskBranch);

    // Delete the task
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}`, { method: 'DELETE' });
    }, { pid: projectId, tid: taskId });

    // Branch should now be deleted
    const branchesAfterDelete = execSync(`git -C ${TEST_REPO} branch --list ${taskBranch}`).toString().trim();
    expect(branchesAfterDelete).not.toContain(taskBranch);
  });

  test('should handle concurrent commit-and-done without errors', async ({ page }) => {
    test.setTimeout(90000);
    const projectId = await getGitProjectId(page);
    const fileSuffix = Date.now();

    // Create two tasks
    const taskId1 = await createTaskViaApi(page, projectId, `concurrent-commit-1-${fileSuffix}`);
    const taskId2 = await createTaskViaApi(page, projectId, `concurrent-commit-2-${fileSuffix}`);

    // Start both tasks to create worktrees
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId1 });
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId2 });

    // Wait for worktrees to be created
    await page.waitForTimeout(2000);

    // Make changes in both worktrees with unique filenames
    const worktreePath1 = `${TEST_WORKTREES}/${taskId1}`;
    const worktreePath2 = `${TEST_WORKTREES}/${taskId2}`;
    const fileName1 = `concurrent-test-${fileSuffix}-1.txt`;
    const fileName2 = `concurrent-test-${fileSuffix}-2.txt`;

    if (fs.existsSync(worktreePath1)) {
      fs.writeFileSync(`${worktreePath1}/${fileName1}`, `Task 1 changes ${fileSuffix}\n`);
      execSync(`git add ${fileName1}`, { cwd: worktreePath1, stdio: 'inherit' });
      execSync(`git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Task 1"`, { cwd: worktreePath1, stdio: 'inherit' });
    }
    if (fs.existsSync(worktreePath2)) {
      fs.writeFileSync(`${worktreePath2}/${fileName2}`, `Task 2 changes ${fileSuffix}\n`);
      execSync(`git add ${fileName2}`, { cwd: worktreePath2, stdio: 'inherit' });
      execSync(`git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Task 2"`, { cwd: worktreePath2, stdio: 'inherit' });
    }

    // Move both tasks to review
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId1 });
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId2 });

    // Get origin branch
    const projectData = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects`);
      const projects = await res.json();
      return projects.find((p: any) => p.id === pid);
    }, projectId);
    const originBranch = projectData.origin_branch;

    // Record commits before
    const logBefore = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const commitsBefore = logBefore ? logBefore.split('\n').length : 0;

    // Open first task and click commit-and-done
    await page.goto(`/?task=${taskId1}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const commitButton1 = page.locator('.btn-commit-done');
    await expect(commitButton1).toBeVisible({ timeout: 5000 });

    // Quickly open second task and click commit-and-done (simulating rapid clicks)
    const page2 = await page.context().newPage();
    await page2.goto(`/?task=${taskId2}`);
    await expect(page2.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const commitButton2 = page2.locator('.btn-commit-done');
    await expect(commitButton2).toBeVisible({ timeout: 5000 });

    // Click both buttons in quick succession
    await commitButton1.click();
    await commitButton2.click();

    // Wait for both to complete
    await expect(commitButton1).not.toBeVisible({ timeout: 30000 });
    await expect(commitButton2).not.toBeVisible({ timeout: 30000 });

    // Verify both tasks are done
    const updatedTask1 = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId1 });
    expect(updatedTask1.status).toBe('done');

    const updatedTask2 = await page2.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId2 });
    expect(updatedTask2.status).toBe('done');

    // Verify merge commits exist
    const logAfter = execSync(`git -C ${TEST_REPO} log --format=%H ${originBranch}`).toString().trim();
    const commitsAfter = logAfter ? logAfter.split('\n').length : 0;
    expect(commitsAfter).toBeGreaterThan(commitsBefore);

    // Verify both file changes are on the origin branch
    const file1Content = execSync(`git -C ${TEST_REPO} show ${originBranch}:${fileName1}`).toString().trim();
    expect(file1Content).toContain(`Task 1 changes ${fileSuffix}`);
    const file2Content = execSync(`git -C ${TEST_REPO} show ${originBranch}:${fileName2}`).toString().trim();
    expect(file2Content).toContain(`Task 2 changes ${fileSuffix}`);

    await page2.close();
  });
});
