import { test, expect } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Project ID generation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should use folder basename as project ID', async ({ page }) => {
    // The global-setup creates a project from /tmp/kanban-test-repo
    // Its ID should be the basename: "kanban-test-repo"
    const projects = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      return res.json();
    });

    const testProject = projects.find(
      (p: any) => p.path === '/tmp/kanban-test-repo'
    );
    expect(testProject).toBeDefined();
    expect(testProject.id).toBe('kanban-test-repo');
  });

  test('should use slugified full path on basename collision', async ({ page }) => {
    // Collision test repo is pre-created by global-setup at /tmp/kanban-test-repo-2
    // but we need one with the SAME basename as the existing project.
    // The existing project from global-setup has basename "kanban-test-repo".
    // We use the second test repo which also has a unique basename.
    // Instead, create a project from a path whose basename collides.
    // global-setup already creates /tmp/kanban-test-repo-2 — let's use it
    // and check that its ID is the basename (no collision with kanban-test-repo).
    // For a real collision test, we create a project via API with a colliding path.

    // Create a temp git repo with SAME basename as existing project
    const { execSync } = await import('node:child_process');
    const fs = await import('node:fs');
    // Parent dir is unique, but basename "kanban-test-repo" collides with existing project
    const tempPath = '/tmp/collision-parent/kanban-test-repo';

    if (!fs.existsSync(tempPath)) {
      fs.mkdirSync(tempPath, { recursive: true });
      fs.writeFileSync(`${tempPath}/README.md`, '# Collision Test\n');
      execSync('git init', { cwd: tempPath });
      execSync('git add README.md', { cwd: tempPath });
      execSync(
        'git -c user.email="test@kanban.local" -c user.name="Test" commit -m "init"',
        { cwd: tempPath }
      );
    }

    const result = await page.evaluate(async (path: string) => {
      const res = await fetch('/api/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      return (await res.json()) as any;
    }, tempPath);

    // Basename "kanban-test-repo" is already taken, so it should fall back to slugified full path
    expect(result.id).not.toBe('kanban-test-repo');
    expect(result.id).toBe('tmp-collision-parent-kanban-test-repo');

    // Clean up
    await page.evaluate(async (id: string) => {
      await fetch(`/api/projects/${id}`, { method: 'DELETE' });
    }, result.id);
  });
});
