# TODO: Add ValidateBranchName() for git ref query parameters

## Context
Git argument injection is possible when user-supplied query parameters starting with `-`
are passed directly to `git` commands as arguments. The `types.ValidateBranchName()`
function already exists in `backend/types/validate.go` and checks for empty names and
leading dashes. The four git handlers in `backend/managers/git.go` now use it.

## Tasks — ALL COMPLETE

### 1. HandleLog (line ~87) — project-level git log
- [x] Validate `branch` query param when non-empty before defaulting to "HEAD"
- [x] Return 400 Bad Request on validation failure

### 2. HandleTaskLog (line ~1285) — task-level git log
- [x] Validate `branch` after fallback to `task.Branch` (only when non-empty)
- [x] Return 400 Bad Request on validation failure

### 3. HandleDiff (lines 309-325) — git diff
- [x] Validate `base` and `head` before git commands
- [x] Return 400 Bad Request on validation failure

### 4. HandleTaskDiff (lines 686-707) — git diff for task
- [x] Validate `customBase` and `headRef` before worktree operations
- [x] Return 400 Bad Request on validation failure

### 5. Tests in backend/managers/git_test.go
- [x] Test each handler with dash-prefixed query params → expect 400
- [x] Test valid refs are accepted (200)
- [x] Use test server helpers (store, config, worktree manager)
- [x] All existing tests pass (managers + types packages)
