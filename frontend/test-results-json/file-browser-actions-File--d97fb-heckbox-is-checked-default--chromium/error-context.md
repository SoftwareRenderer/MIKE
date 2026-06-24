# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: file-browser-actions.spec.ts >> File Browser Actions >> should create project with sandbox enabled when checkbox is checked (default)
- Location: tests/e2e/file-browser-actions.spec.ts:139:3

# Error details

```
Error: expect(locator).toBeVisible() failed

Locator: locator('.project-item .sandbox-badge:not(.sandbox-badge-off)')
Expected: visible
Error: strict mode violation: locator('.project-item .sandbox-badge:not(.sandbox-badge-off)') resolved to 2 elements:
    1) <span title="Sandbox enabled (gVisor)" class="sandbox-badge svelte-xo6w1v" aria-label="Sandbox enabled (gVisor)">…</span> aka getByRole('button', { name: 'Sandbox enabled (gVisor) kanban-test-nongit' })
    2) <span title="Sandbox enabled (gVisor)" class="sandbox-badge svelte-xo6w1v" aria-label="Sandbox enabled (gVisor)">…</span> aka getByRole('button', { name: 'Sandbox enabled (gVisor) kanban-test-sandbox-on-' })

Call log:
  - Expect "toBeVisible" with timeout 5000ms
  - waiting for locator('.project-item .sandbox-badge:not(.sandbox-badge-off)')

```

# Page snapshot

```yaml
- generic [ref=e3]:
  - link "Skip to main content" [ref=e4] [cursor=pointer]:
    - /url: "#main-content"
  - complementary "Sidebar" [ref=e5]:
    - generic [ref=e6]:
      - heading "Kanban" [level=2] [ref=e7]
      - button "Collapse sidebar" [expanded] [ref=e8] [cursor=pointer]: ◂
    - navigation "Projects" [ref=e9]:
      - generic [ref=e10]:
        - button "Sandbox disabled test-project 2 0 0" [ref=e11] [cursor=pointer]:
          - generic "Sandbox disabled" [ref=e12]
          - generic [ref=e13]: test-project
          - generic [ref=e14]:
            - generic "Backlog" [ref=e15]:
              - img [ref=e16]
              - text: "2"
            - generic "In Progress" [ref=e18]:
              - img [ref=e19]
              - text: "0"
            - generic "Review" [ref=e21]:
              - img [ref=e22]
              - text: "0"
        - button "Remove project test-project" [ref=e25] [cursor=pointer]: ✕
      - generic [ref=e26]:
        - button "Sandbox enabled (gVisor) kanban-test-nongit" [ref=e27] [cursor=pointer]:
          - generic "Sandbox enabled (gVisor)" [ref=e28]
          - generic [ref=e29]: kanban-test-nongit
        - button "Remove project kanban-test-nongit" [ref=e30] [cursor=pointer]: ✕
      - generic [ref=e31]:
        - button "Sandbox enabled (gVisor) kanban-test-sandbox-on-1782258894300" [ref=e32] [cursor=pointer]:
          - generic "Sandbox enabled (gVisor)" [ref=e33]
          - generic [ref=e34]: kanban-test-sandbox-on-1782258894300
        - button "Remove project kanban-test-sandbox-on-1782258894300" [ref=e35] [cursor=pointer]: ✕
    - button "Add Project" [ref=e37] [cursor=pointer]: + Project
  - main [ref=e38]:
    - generic [ref=e39]:
      - button "Sandbox configuration" [ref=e40] [cursor=pointer]
      - generic [ref=e41]: kanban-test-sandbox-on-1782258894300 (master)
      - generic [ref=e42]:
        - button "Open terminal" [ref=e43] [cursor=pointer]:
          - img [ref=e44]
        - button "Token usage stats" [ref=e47] [cursor=pointer]:
          - img [ref=e48]
        - button "Settings" [ref=e52] [cursor=pointer]:
          - img [ref=e53]
    - region "Kanban board" [ref=e56]:
      - region "Task board" [ref=e57]:
        - list "Backlog column" [ref=e58]:
          - generic [ref=e59]:
            - heading "Backlog" [level=3] [ref=e60]:
              - img [ref=e61]
              - text: Backlog
            - generic [ref=e64]: "0"
          - generic [ref=e65]:
            - button "+ Add Task" [ref=e66] [cursor=pointer]
            - 'button "Open task: Test task for e2e tests" [ref=e68]':
              - generic [ref=e69]:
                - generic [ref=e70]: Test task for e2e tests
                - generic:
                  - button "Create subtask":
                    - img
                  - button "Edit task":
                    - img
                  - button "Move to In Progress":
                    - img
              - button "Change model" [ref=e72] [cursor=pointer]:
                - generic [ref=e73]: mock-model
                - img [ref=e74]
            - 'button "Open task: Second test task" [ref=e77]':
              - generic [ref=e78]:
                - generic [ref=e79]: Second test task
                - generic:
                  - button "Create subtask":
                    - img
                  - button "Edit task":
                    - img
                  - button "Move to In Progress":
                    - img
              - button "Change model" [ref=e81] [cursor=pointer]:
                - generic [ref=e82]: mock-model
                - img [ref=e83]
        - list "In Progress column" [ref=e85]:
          - generic [ref=e86]:
            - heading "In Progress" [level=3] [ref=e87]:
              - img [ref=e88]
              - text: In Progress
            - generic [ref=e91]: "0"
        - list "Review column" [ref=e93]:
          - generic [ref=e94]:
            - heading "Review" [level=3] [ref=e95]:
              - img [ref=e96]
              - text: Review
            - generic [ref=e100]: "0"
        - list "Done column" [ref=e102]:
          - generic [ref=e103]:
            - heading "Done" [level=3] [ref=e104]:
              - img [ref=e105]
              - text: Done
            - generic [ref=e108]: "0"
```

# Test source

```ts
  84  | 
  85  |     await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });
  86  | 
  87  |     const folderName = `test-folder-${Date.now()}`;
  88  |     await page.fill('.folder-name-input', folderName);
  89  |     await page.locator('.folder-name-input').press('Enter');
  90  | 
  91  |     // Wait for the new folder input to disappear (navigated into new folder)
  92  |     await expect(page.locator('.folder-name-input')).toBeHidden({ timeout: 5000 });
  93  | 
  94  |     // Current path should now include the new folder name
  95  |     const currentPathText = await page.locator('.current-path').textContent();
  96  |     expect(currentPathText).toContain(folderName);
  97  | 
  98  |     // Close the browser
  99  |     await page.click('.btn-cancel');
  100 |     await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });
  101 |   });
  102 | 
  103 |   test('should show error when creating a folder that already exists', async ({ page }) => {
  104 |     await page.click('.add-project-btn');
  105 |     await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });
  106 | 
  107 |     const pathInput = page.locator('.path-input');
  108 |     await pathInput.fill(TEST_NEW_FOLDER_PARENT);
  109 |     await pathInput.press('Enter');
  110 |     await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });
  111 | 
  112 |     // Open new folder dialog
  113 |     await page.click('.icon-btn-sm[title="New folder"]');
  114 |     await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });
  115 | 
  116 |     // Create a folder
  117 |     const folderName = `dup-folder-${Date.now()}`;
  118 |     await page.fill('.folder-name-input', folderName);
  119 |     await page.locator('.folder-name-input').press('Enter');
  120 |     await expect(page.locator('.folder-name-input')).toBeHidden({ timeout: 5000 });
  121 | 
  122 |     // Go back to parent
  123 |     await pathInput.fill(TEST_NEW_FOLDER_PARENT);
  124 |     await pathInput.press('Enter');
  125 |     await expect(page.locator('.file-list')).toBeVisible({ timeout: 5000 });
  126 | 
  127 |     // Try to create the same folder again
  128 |     await page.click('.icon-btn-sm[title="New folder"]');
  129 |     await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });
  130 |     await page.fill('.folder-name-input', folderName);
  131 |     await page.locator('.folder-name-input').press('Enter');
  132 | 
  133 |     // Should show an error
  134 |     await expect(page.locator('.error')).toBeVisible({ timeout: 5000 });
  135 |     const errorText = await page.locator('.error').textContent();
  136 |     expect(errorText).toContain('already exists');
  137 |   });
  138 | 
  139 |   test('should create project with sandbox enabled when checkbox is checked (default)', async ({ page }) => {
  140 |     const initialCount = await page.locator('.project-item').count();
  141 |     const uniqueDir = `/tmp/kanban-test-sandbox-on-${Date.now()}`;
  142 |     try { rmSync(uniqueDir, { recursive: true, force: true }); } catch { /* ignore */ }
  143 |     mkdirSync(uniqueDir, { recursive: true });
  144 |     execSync('git init', { cwd: uniqueDir });
  145 |     execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: uniqueDir });
  146 | 
  147 |     // Open the file browser
  148 |     await page.click('.add-project-btn');
  149 |     await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });
  150 | 
  151 |     // Verify "Use Sandbox" checkbox is checked by default
  152 |     const sandboxCheckbox = page.locator('.sandbox-toggle input[type="checkbox"]');
  153 |     await expect(sandboxCheckbox).toBeChecked();
  154 | 
  155 |     // Navigate to the test git directory
  156 |     const pathInput = page.locator('.path-input');
  157 |     await pathInput.fill(uniqueDir);
  158 |     await pathInput.press('Enter');
  159 | 
  160 |     // Wait for git indicator
  161 |     await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });
  162 | 
  163 |     // Click Select
  164 |     await page.click('.btn-select');
  165 | 
  166 |     // Browser should close
  167 |     await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });
  168 | 
  169 |     // New project should be added
  170 |     await expect(page.locator('.project-item')).toHaveCount(initialCount + 1, { timeout: 5000 });
  171 | 
  172 |     // Verify via API that the last project has sandbox_enabled: true
  173 |     await expect(async () => {
  174 |         const projects = await page.evaluate(async () => {
  175 |             const res = await fetch('/api/projects');
  176 |             return res.json();
  177 |         });
  178 |         const lastProject = projects[projects.length - 1];
  179 |         expect(lastProject.sandbox_enabled).toBe(true);
  180 |     }).toPass({ timeout: 5000 });
  181 | 
  182 |     // Verify UI shows sandbox badge (not sandbox-badge-off)
  183 |     // The newly selected project should show the active sandbox badge
> 184 |     await expect(page.locator('.project-item .sandbox-badge:not(.sandbox-badge-off)')).toBeVisible({ timeout: 5000 });
      |                                                                                        ^ Error: expect(locator).toBeVisible() failed
  185 |   });
  186 | 
  187 |   test('should create project with sandbox disabled when checkbox is unchecked', async ({ page }) => {
  188 |     const initialCount = await page.locator('.project-item').count();
  189 | 
  190 |     // Open the file browser
  191 |     await page.click('.add-project-btn');
  192 |     await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });
  193 | 
  194 |     // Uncheck "Use Sandbox"
  195 |     const sandboxCheckbox = page.locator('.sandbox-toggle input[type="checkbox"]');
  196 |     await sandboxCheckbox.click();
  197 |     await expect(sandboxCheckbox).not.toBeChecked();
  198 | 
  199 |     // Navigate to a unique directory
  200 |     const uniqueDir = `/tmp/kanban-test-sandbox-off-${Date.now()}`;
  201 |     try { rmSync(uniqueDir, { recursive: true, force: true }); } catch { /* ignore */ }
  202 |     mkdirSync(uniqueDir, { recursive: true });
  203 |     execSync('git init', { cwd: uniqueDir });
  204 |     execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: uniqueDir });
  205 | 
  206 |     const pathInput = page.locator('.path-input');
  207 |     await pathInput.fill(uniqueDir);
  208 |     await pathInput.press('Enter');
  209 | 
  210 |     // Wait for git indicator
  211 |     await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });
  212 | 
  213 |     // Click Select
  214 |     await page.click('.btn-select');
  215 | 
  216 |     // Browser should close
  217 |     await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });
  218 | 
  219 |     // New project should be added
  220 |     await expect(page.locator('.project-item')).toHaveCount(initialCount + 1, { timeout: 5000 });
  221 | 
  222 |     // Verify via API that the last project has sandbox_enabled: false
  223 |     await expect(async () => {
  224 |         const projects = await page.evaluate(async () => {
  225 |             const res = await fetch('/api/projects');
  226 |             return res.json();
  227 |         });
  228 |         const lastProject = projects[projects.length - 1];
  229 |         expect(lastProject.sandbox_enabled).toBe(false);
  230 |     }).toPass({ timeout: 5000 });
  231 |   });
  232 | });
  233 | 
```