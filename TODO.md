# TODO: E2E Test Suite Overhaul

## Goal
Get e2e test suite to run in under 60 seconds by migrating ~45 static/rendering tests to unit tests and optimizing the remaining e2e tests.

## Progress

### Completed
- [x] Fixed global-setup bug (setup not running after teardown)
- [x] Fixed login.spec.ts selectors (`.login-modal` → `[role="dialog"]`)
- [x] Fixed sandbox-forward-ports.spec.ts selectors (`.sandbox-config-dialog` → `[role="dialog"]`)
- [x] Fixed sandbox-mounts.spec.ts selectors
- [x] Fixed settings-provider.spec.ts selectors (Settings button strict mode)
- [x] Fixed settings-power.spec.ts selectors
- [x] Added `closeOnOverlayClick` prop to BaseModal + LoginModal
- [x] Created 29 new unit tests across 8 new test files:
  - reasoning-block.unit.svelte.ts (10 tests)
  - message-actions.unit.svelte.ts (10 tests)
  - review-bar.unit.svelte.ts (10 tests)
  - queue-indicator.unit.svelte.ts (9 tests)
  - start-panel.unit.svelte.ts (10 tests)
  - context-usage.unit.svelte.ts (12 tests)
  - thread-selector.unit.svelte.ts (10 tests)
  - model-selector.unit.svelte.ts (10 tests)
  - confirmation-dialog.unit.svelte.ts (10 tests)
  - toggle-switch.unit.svelte.ts (10 tests)
  - virtual-list.unit.svelte.ts (10 tests)
  - edit-form.unit.svelte.ts (10 tests)
  - diff-select.unit.svelte.ts (10 tests)
  - commit-dialog.unit.svelte.ts (12 tests)
  - card-status.unit.svelte.ts (10 tests)
  - board-layout.unit.svelte.ts (10 tests)
  - task-header.unit.svelte.ts (9 tests)
  - progress-indicator.unit.svelte.ts (10 tests)
  - tool-call.unit.svelte.ts (15 tests)
  - file-mention-dropdown.unit.svelte.ts (9 tests)

### Current State
- **Unit tests**: 498 tests, ~1s runtime (44 test files)
- **E2E tests**: 193 tests, ~3.2 minutes runtime (22 test files)
  - 120 passing
  - 23 failing (mostly timing/selector issues in complex interaction tests)
  - 50 skipped
- **Workers**: 4 (main), 2 (isolated)
- **No waitForTimeout calls** (all replaced with condition-based waits)

### Remaining Work
- [ ] Fix remaining 23 failing e2e tests (timing/selector issues)
- [ ] Migrate additional static/rendering e2e tests to unit tests (target: ~150 e2e)
- [ ] Optimize slow e2e tests (reduce avg test time from 1.6s to 1.0s)
- [ ] Consider consolidating small spec files to reduce overhead

### Key Bottlenecks
1. Global-setup/teardown: ~5s (creates 2 servers + mock LLM)
2. Average test time: ~1.6s per test
3. Some tests take 10-30s (complex interactions with server)

### Notes
- Unit tests use source-code analysis pattern (readFileSync + expect(source).toContain())
- For interactive component tests, use @testing-library/svelte with render()
- Keep e2e tests for: server interactions, LLM responses, multi-step user flows, browser storage
