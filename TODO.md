# TODO

## Goal
Migrate ~45 e2e tests to unit tests (vitest + @testing-library/svelte) and clean up the corresponding e2e spec files.

## Tasks

### 1. Create test directory
- [x] Create `frontend/src/lib/components/__tests__/` directory

### 2. Create `accessibility.unit.svelte.ts` (15 tests)
- [x] Skip link, settings modal role/aria-modal, focus trapping, card keyboard access, toggle switches, icon button aria-labels, sidebar toggle aria-expanded, expand/collapse aria-expanded, focus restoration, decorative SVGs, main landmark, board aria-label, sidebar nav aria-label, column ARIA roles

### 3. Create `stats-modal.unit.svelte.ts` (2 tests)
- [x] Cost calculation test, cache token display

### 4. Create `diff-rendering.unit.svelte.ts` (6 tests)
- [x] Unstaged blank file, staged blank file, blank removed/added lines, diff panel clears on untracked remove, section collapse, section expand

### 5. Create `search-panel.unit.svelte.ts` (7 tests)
- [x] Open search panel, find matches, zero matches, close with Escape, navigate between matches, toggle replace panel, close with button

### 6. Create `toast.unit.svelte.ts` (4 tests)
- [x] Error styling, success styling, auto-dismiss, toast replacement

### 7. Create `multi-select-state.unit.svelte.ts` (5 tests)
- [x] Toggle selection with Ctrl+Click, action bar visibility, clear selection button, hide action bar (1 selected), clear on empty board click

### 8. Create `board-overview.unit.svelte.ts` (3 tests)
- [x] 4 sections visible, sections when empty, collapsible sections with chevron

### 9. Create `pwa-manifest.unit.ts` (1 test)
- [x] Verify manifest.json exists and has required fields

### 10. Create `sw-update-banner.unit.svelte.ts` (1 test)
- [x] Banner visibility conditional

### 11. Create `sidebar-color.unit.svelte.ts` (1 test)
- [x] CSS class check on collapsed sidebar

### 12. Clean up e2e test files
- [x] Remove migrated tests from `accessibility.spec.ts` (deleted)
- [x] Remove migrated tests from `stats-modal.spec.ts`
- [x] Remove migrated tests from `diff-viewer.spec.ts`
- [x] Remove migrated tests from `search.spec.ts` (deleted)
- [x] Remove migrated tests from `toast.spec.ts` (deleted)
- [x] Remove migrated tests from `multi-select.spec.ts`
- [x] Remove migrated tests from `board-overview.spec.ts`
- [x] Remove migrated test from `pwa-manifest.spec.ts` (deleted)
- [x] Remove migrated test from `sw-update-banner.spec.ts` (deleted)
- [x] Remove migrated test from `sidebar-collapsed-abbrev-color.spec.ts` (deleted)
- [x] Delete empty e2e spec files

### 13. Verification
- [x] Run `cd frontend && npm test` — all new unit tests must pass
- [x] Run `cd frontend && npx playwright test --list` — migrated tests should no longer appear
