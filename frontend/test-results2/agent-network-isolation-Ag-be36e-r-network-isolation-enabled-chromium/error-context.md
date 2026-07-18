# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: agent-network-isolation.spec.ts >> Agent + Network Isolation >> should block task start when agent provider + network isolation enabled
- Location: tests/e2e/agent-network-isolation.spec.ts:10:3

# Error details

```
TimeoutError: page.waitForSelector: Timeout 10000ms exceeded.
Call log:
  - waiting for locator('.project-item') to be visible

```

# Page snapshot

```yaml
- generic [ref=e2]: 404 page not found
```

# Test source

```ts
  1   | import { test, expect } from '@playwright/test';
  2   | import { getProjectId, csrfFetch } from './helpers';
  3   | 
  4   | test.describe.serial('Agent + Network Isolation', () => {
  5   |   test.beforeEach(async ({ page }) => {
  6   |     await page.goto('/');
> 7   |     await page.waitForSelector('.project-item', { timeout: 10000 });
      |                ^ TimeoutError: page.waitForSelector: Timeout 10000ms exceeded.
  8   |   });
  9   | 
  10  |   test('should block task start when agent provider + network isolation enabled', async ({ page }) => {
  11  |     const projectId = await getProjectId(page);
  12  | 
  13  |     // Enable sandbox + network isolation on the project
  14  |     await csrfFetch(page, `/api/projects/${projectId}`, {
  15  |       method: 'PUT',
  16  |       body: { sandbox_enabled: true, sandbox_network_isolation: true },
  17  |     });
  18  | 
  19  |     // Verify network isolation is enabled
  20  |     const projectData = await page.evaluate(async (pid) => {
  21  |       const res = await fetch(`/api/projects/${pid}`);
  22  |       return res.ok ? res.json() : null;
  23  |     }, projectId);
  24  |     expect(projectData.sandbox_network_isolation).toBe(true);
  25  | 
  26  |     // Create a task with a Pi agent provider (pi://agent sentinel URL)
  27  |     const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
  28  |       method: 'POST',
  29  |       body: {
  30  |         prompt: `agent-network-test-${Date.now()}`,
  31  |         llama_server_url: 'pi://agent',
  32  |         model_name: null,
  33  |       },
  34  |     });
  35  |     expect(result.ok).toBe(true);
  36  |     const taskId = result.data?.id;
  37  |     expect(taskId).toBeTruthy();
  38  | 
  39  |     // Try to start the task — should fail with agent error
  40  |     const startResult = await csrfFetch(
  41  |       page,
  42  |       `/api/projects/${projectId}/tasks/${taskId}/start`,
  43  |       { method: 'POST' }
  44  |     );
  45  |     expect(startResult.ok).toBe(false);
  46  |     expect(startResult.status).toBe(500);
  47  |     expect(startResult.data?.error).toContain('agent requires network to be enabled');
  48  | 
  49  |     // Verify task is still in backlog (not started)
  50  |     const taskData = await page.evaluate(async ({ pid, tid }) => {
  51  |       const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
  52  |       return res.ok ? res.json() : null;
  53  |     }, { pid: projectId, tid: taskId });
  54  |     expect(taskData.status).toBe('backlog');
  55  |   });
  56  | 
  57  |   test('should allow task start when agent provider + network isolation disabled', async ({ page }) => {
  58  |     const projectId = await getProjectId(page);
  59  | 
  60  |     // Ensure network isolation is disabled
  61  |     await csrfFetch(page, `/api/projects/${projectId}`, {
  62  |       method: 'PUT',
  63  |       body: { sandbox_enabled: false, sandbox_network_isolation: false },
  64  |     });
  65  | 
  66  |     // Create a task with a Pi agent provider
  67  |     const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
  68  |       method: 'POST',
  69  |       body: {
  70  |         prompt: `agent-allowed-test-${Date.now()}`,
  71  |         llama_server_url: 'pi://agent',
  72  |         model_name: null,
  73  |       },
  74  |     });
  75  |     expect(result.ok).toBe(true);
  76  |     const taskId = result.data?.id;
  77  | 
  78  |     // Start the task — should succeed (no network isolation)
  79  |     const startResult = await csrfFetch(
  80  |       page,
  81  |       `/api/projects/${projectId}/tasks/${taskId}/start`,
  82  |       { method: 'POST' }
  83  |     );
  84  |     expect(startResult.ok).toBe(true);
  85  | 
  86  |     // Verify task is in_progress
  87  |     const taskData = await page.evaluate(async ({ pid, tid }) => {
  88  |       const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
  89  |       return res.ok ? res.json() : null;
  90  |     }, { pid: projectId, tid: taskId });
  91  |     expect(taskData.status).toBe('in_progress');
  92  |   });
  93  | 
  94  |   test('should allow task start when LLM provider + network isolation enabled', async ({ page }) => {
  95  |     const projectId = await getProjectId(page);
  96  | 
  97  |     // Stop any running tasks from previous tests (serial suite shared state)
  98  |     const tasks = await page.evaluate(async (pid) => {
  99  |       const res = await fetch(`/api/projects/${pid}/tasks`);
  100 |       return res.ok ? res.json() : [];
  101 |     }, projectId);
  102 |     for (const task of tasks) {
  103 |       if (task.status === 'in_progress') {
  104 |         await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
  105 |           method: 'POST',
  106 |           body: { status: 'backlog' },
  107 |         });
```