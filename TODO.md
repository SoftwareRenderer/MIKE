# TODO: Add ValidateBranchName() for git ref query parameters

## Context
Git argument injection is possible when user-supplied query parameters starting with `-`
are passed directly to `git` commands as arguments. The `types.ValidateBranchName()`
function in `backend/types/validate.go` was enhanced to not only check for leading
hyphens but also validate that only allowed characters are present (letters, digits,
-, _, ., /, +) and reject consecutive dots (..).

## Tasks — ALL COMPLETE

### 1. HandleLog (line ~87) — project-level git log
- [x] Validate `branch` query param when non-empty before defaulting to "HEAD"
- [x] Return 400 Bad Request on validation failure

### 2. HandleTaskLog (line ~1315) — task-level git log
- [x] Validate `branch` after fallback to `task.Branch` (only when non-empty)
- [x] Return 400 Bad Request on validation failure

### 3. HandleDiff (lines 309-325) — git diff
- [x] Validate `base` and `head` before git commands
- [x] Return 400 Bad Request on validation failure

### 4. HandleTaskDiff (lines 686-707) — git diff for task
- [x] Validate `customBase` and `headRef` before worktree operations
- [x] Return 400 Bad Request on validation failure

### 5. Enhanced ValidateBranchName (backend/types/validate.go)
- [x] Added character whitelist: only `a-zA-Z0-9-_.+/` allowed
- [x] Added `..` (consecutive dots) check
- [x] Kept existing empty and leading-dash checks
- [x] Updated tests with valid/invalid character cases

### 6. Tests (backend/managers/git_test.go + backend/types/validate_test.go)
- [x] Unit tests for ValidateBranchName with 22 test cases (8 valid, 14 invalid)
- [x] Handler-level tests for all 4 handlers (dash prefix → 400, valid → 200)
- [x] Character validation tests for all 4 handlers (~ ^ : ? * [ \ space .. → 400)
- [x] All existing tests pass (managers, types, exec, git, store packages)
