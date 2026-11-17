package exec

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"errors"
	"mike/backend/config"
	"mike/backend/errs"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
	pkgagent "mike/pkg/agent"
)

// setupExecutorWithGit creates a TaskExecutor with a real git repo for testing.
func setupExecutorWithGit(t *testing.T) (*TaskExecutor, string, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(worktreesDir, 0o755)

	testgit.RepoAtMessage(t, repoDir, "Initial")
	initialSHA := testgit.GitOutput(t, repoDir, "rev-parse", "HEAD")

	// Create store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	// Create worktree manager and executor
	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := newTestStreamMgr(t, s, worktreeMgr)

	te := newTestExecutorWith(t, Deps{
		Store:        s,
		ConfigStore:  cfgStore,
		AppStreamMgr: appStreamMgr,
		WorktreeMgr:  worktreeMgr,
		CommitQueue:  NewCommitQueue(),
	})

	return te, repoDir, projectID, initialSHA
}

// TestPrepareTaskExecution_BaseRefIsWrittenOnce checks both halves of one decision: the base ref is
// recorded from the branch on the first run and never rewritten afterwards — a restart that replaced the
// stored commit with the branch name loses the point the task diverged from.
func TestPrepareTaskExecution_BaseRefIsWrittenOnce(t *testing.T) {
	t.Parallel()
	te, _, projectID, initialSHA := setupExecutorWithGit(t)

	t.Run("first run records the base", func(t *testing.T) {
		taskID := "test-baseref-first-run"
		if err := te.store.CreateTask(&types.Task{
			ID: taskID, ProjectID: projectID, Title: "Test task", Prompt: "Test prompt",
			Status: types.StatusInProgress, Branch: "task/" + taskID,
			LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		}); err != nil {
			t.Fatalf("create task: %v", err)
		}

		setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
		if err != nil {
			t.Fatalf("prepare task execution: %v", err)
		}
		if setup == nil {
			t.Fatal("prepare task execution returned nil")
		}
		defer te.releasePrepared(setup)

		if setup.task.BaseRef == "" {
			t.Error("BaseRef should be set on first run")
		}
		if len(setup.task.BaseRef) == 40 {
			t.Errorf("BaseRef should be a branch name, not a 40-char SHA, got %q", setup.task.BaseRef)
		}
	})

	t.Run("restart keeps the stored commit", func(t *testing.T) {
		taskID := "test-baseref-task"
		branchName := "task/" + taskID
		if err := te.store.CreateTask(&types.Task{
			ID: taskID, ProjectID: projectID, Title: "Test task", Prompt: "Test prompt",
			Status: types.StatusInProgress, BaseRef: initialSHA, Branch: branchName,
			LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		}); err != nil {
			t.Fatalf("create task: %v", err)
		}
		// The worktree exists: this is the second start of a task that already ran.
		if _, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, ""); err != nil {
			t.Fatalf("create worktree: %v", err)
		}

		setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
		if err != nil {
			t.Fatalf("prepare task execution: %v", err)
		}
		if setup == nil {
			t.Fatal("prepare task execution returned nil")
		}
		defer te.releasePrepared(setup)

		if setup.task.BaseRef != initialSHA {
			t.Errorf("BaseRef was overwritten on restart: expected %q (SHA), got %q", initialSHA, setup.task.BaseRef)
		}
		if len(setup.task.BaseRef) != 40 {
			t.Errorf("BaseRef should be a 40-char SHA, got %q (len=%d)", setup.task.BaseRef, len(setup.task.BaseRef))
		}
	})
}

// TestInitTask_CreatesWorktree verifies that InitTask creates the worktree
// and sets branch/BaseRef without starting LLM execution.
func TestInitTask_CreatesWorktree(t *testing.T) {
	t.Parallel()
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-init-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    types.StatusBacklog,
	}
	te.store.CreateTask(task)

	// Init the task
	result, err := te.InitTask(projectID, taskID)
	if err != nil {
		t.Fatalf("InitTask failed: %v", err)
	}

	// Verify branch is set
	if result.Branch == "" {
		t.Error("Branch should be set after init")
	}

	// Verify BaseRef is set
	if result.BaseRef == "" {
		t.Error("BaseRef should be set after init")
	}

	// Verify BaseCommit is set to the initial SHA
	if result.BaseCommit != initialSHA {
		t.Errorf("BaseCommit should be set to initial SHA: expected %q, got %q", initialSHA, result.BaseCommit)
	}

	// Verify worktree directory exists
	wtPath, _ := te.worktreeMgr.WorktreePath(taskID)
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree directory should exist at %s: %v", wtPath, err)
	}

	// Verify task status is unchanged (init should NOT move to in_progress)
	reloaded, _ := te.store.GetTaskByID(taskID)
	if reloaded.Status != types.StatusBacklog {
		t.Errorf("Status should remain backlog after init, got %q", reloaded.Status)
	}
	if reloaded.ExecutionState == types.ExecStateRunning {
		t.Errorf("ExecutionState should not be running after init, got %q", reloaded.ExecutionState)
	}
}

// TestInitTask_Idempotent verifies that calling InitTask twice is safe.
func TestInitTask_Idempotent(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-init-idempotent-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    types.StatusBacklog,
	}
	te.store.CreateTask(task)

	// First init
	result1, err := te.InitTask(projectID, taskID)
	if err != nil {
		t.Fatalf("first InitTask failed: %v", err)
	}

	// Second init (should be a no-op)
	result2, err := te.InitTask(projectID, taskID)
	if err != nil {
		t.Fatalf("second InitTask failed: %v", err)
	}

	// Branch should be the same
	if result1.Branch != result2.Branch {
		t.Errorf("Branch should be the same on second init: %q vs %q", result1.Branch, result2.Branch)
	}

	// Status should still be backlog
	if result2.Status != types.StatusBacklog {
		t.Errorf("Status should remain backlog after second init, got %q", result2.Status)
	}
}

// TestInitTask_ThenPrepareTaskExecution verifies that prepareTaskExecution
// works correctly after InitTask has already created the worktree.
func TestInitTask_ThenPrepareTaskExecution(t *testing.T) {
	t.Parallel()
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-init-then-start-task"

	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test task",
		Prompt:         "Test prompt",
		Status:         types.StatusBacklog,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(task)

	// Init the task first
	initResult, err := te.InitTask(projectID, taskID)
	if err != nil {
		t.Fatalf("InitTask failed: %v", err)
	}
	initBranch := initResult.Branch

	// Now prepare for execution (simulating start)
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if err != nil {
		t.Fatalf("prepareTaskExecution after init failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}

	// Branch should be preserved from init
	if setup.task.Branch != initBranch {
		t.Errorf("Branch should be preserved from init: expected %q, got %q", initBranch, setup.task.Branch)
	}

	// BaseRef should be preserved
	if setup.task.BaseRef == "" {
		t.Error("BaseRef should be set")
	}

	if setup.task.BaseCommit != initialSHA {
		t.Errorf("BaseCommit should be preserved: expected %q, got %q", initialSHA, setup.task.BaseCommit)
	}

	te.releasePrepared(setup)
}

// TestHasRunningTasks holds the one conjunction behind "is this project busy": a task counts only when
// its stored status is in_progress AND this process holds an executor for it. Either half alone is not a
// running task, and asking never cleans up — removing a stale entry is StopTask's job.
func TestHasRunningTasks(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	now := time.Now().UTC()
	if err := te.store.CreateProject(&types.Project{ID: "hr-empty", Name: "empty", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create empty project: %v", err)
	}
	for _, spec := range []struct {
		id     string
		status types.Status
	}{
		{"hr-in-progress", types.StatusInProgress},
		{"hr-backlog", types.StatusBacklog},
		{"hr-review", types.StatusReview},
	} {
		if err := te.store.CreateTask(&types.Task{
			ID: spec.id, ProjectID: projectID, Title: spec.id, Status: spec.status,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", spec.id, err)
		}
	}

	// hold puts exactly these tasks into the executor map, which is the half the store cannot answer.
	hold := func(ids ...string) {
		te.taskMu.Lock()
		defer te.taskMu.Unlock()
		te.tasks = map[string]*executionState{}
		for _, id := range ids {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			te.tasks[id] = &executionState{ctx: ctx, cancel: cancel, executorType: "llm"}
		}
	}

	for _, tc := range []struct {
		name    string
		project string
		held    []string
		want    bool
	}{
		{"an in_progress task held by an executor", projectID, []string{"hr-in-progress"}, true},
		{"an in_progress task with no executor", projectID, nil, false},
		{"a backlog entry in the map is not a run", projectID, []string{"hr-backlog"}, false},
		{"a review entry in the map is not a run", projectID, []string{"hr-review"}, false},
		{"one run among idle tasks", projectID, []string{"hr-backlog", "hr-in-progress", "hr-review"}, true},
		{"a project with no tasks", "hr-empty", nil, false},
		{"a project that does not exist", "hr-nonexistent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hold(tc.held...)
			if got := te.HasRunningTasks(tc.project); got != tc.want {
				t.Errorf("HasRunningTasks(%q) = %v, want %v", tc.project, got, tc.want)
			}
			te.taskMu.Lock()
			defer te.taskMu.Unlock()
			if len(te.tasks) != len(tc.held) {
				t.Errorf("the answer mutated the executor map: %d entries, want %d", len(te.tasks), len(tc.held))
			}
		})
	}
}

func TestCommitAndMerge_SerializesPerProject(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create a task with a worktree so CommitAndMerge has something to commit
	taskID := "test-serialize-task"
	branchName := "task/" + taskID
	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test serialize",
		Branch:    branchName,
		BaseRef:   "master",
	}
	te.store.CreateTask(task)

	// Create the worktree and make an initial commit
	_, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wtPath, _ := te.worktreeMgr.WorktreePath(taskID)
	os.WriteFile(filepath.Join(wtPath, "initial.txt"), []byte("initial\n"), 0o644)
	testgit.Git(t, wtPath, "add", "initial.txt")
	testgit.Git(t, wtPath, "commit", "-m", "initial")

	// Track execution order
	var order []string
	var mu sync.Mutex

	// Monkey-patch: we can't easily mock gitRun, so we verify serialization
	// by checking that concurrent CommitAndMerge calls don't interleave.
	// We write unique files in each call and verify they commit separately.
	// Use a channel to coordinate: first call writes file-a, second writes file-b.
	startFirst := make(chan struct{})
	startSecond := make(chan struct{})
	firstDone := make(chan struct{})

	var wg sync.WaitGroup

	// First call — writes "file-a" content
	wg.Go(func() {
		// Make a change before committing
		os.WriteFile(filepath.Join(wtPath, "file-a"), []byte("content-a\n"), 0o644)
		testgit.Git(t, wtPath, "add", "file-a")
		close(startFirst)
		te.merge.CommitAndMerge(projectID, taskID, wtPath, "commit-a", false)
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		close(firstDone)
	})

	// Wait for first to enter the commit (be inside CommitAndMerge)
	<-startFirst
	// Small delay to ensure first is inside the queue
	time.Sleep(50 * time.Millisecond)

	// Second call — writes "file-b" content, should block until first releases
	wg.Go(func() {
		os.WriteFile(filepath.Join(wtPath, "file-b"), []byte("content-b\n"), 0o644)
		testgit.Git(t, wtPath, "add", "file-b")
		close(startSecond)
		te.merge.CommitAndMerge(projectID, taskID, wtPath, "commit-b", false)
		mu.Lock()
		order = append(order, "second")
		mu.Unlock()
	})

	<-startSecond
	// Second should be blocked — verify first hasn't finished yet
	select {
	case <-firstDone:
		// First finished very fast, that's OK on fast machines
	default:
		// Expected: first is still running
	}

	wg.Wait()

	// Verify order: first must complete before second
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("expected order [first, second], got %v", order)
	}

	// Verify both commits exist on the branch
	logOutput := testgit.GitOutput(t, repoDir, "log", branchName, "--oneline")
	if !strings.Contains(logOutput, "commit-a") {
		t.Errorf("expected commit-a in branch log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "commit-b") {
		t.Errorf("expected commit-b in branch log, got:\n%s", logOutput)
	}
}

// TestGenerateBranchName pins the naming rule for a task branch: the model's title names it only when a
// person did not ask for an auto-merge (which needs the task id back to find the branch again), an
// unsluggable title falls back to the id, and whatever the pieces are the name stays within the cap.
func TestGenerateBranchName(t *testing.T) {
	t.Parallel()
	const taskID = "frost-amber-quartz"
	// A title that slugifies to more than types.BranchSlugMaxLen characters.
	longTitle := "Fix a very long bug description that exceeds the maximum allowed branch name length limit"

	for _, tc := range []struct {
		name       string
		title      string
		titleLLM   bool
		autoMerge  bool
		wantPrefix string
		wantLen    int
		maxLen     int
	}{
		{
			name: "the model's title names the branch", title: "Fix login", titleLLM: true,
			wantPrefix: "task/fix-login-", wantLen: len("task/fix-login") + 1 + randomSuffixLen,
		},
		{
			name: "an auto-merge task keeps its id in the name", title: "Fix login", titleLLM: true, autoMerge: true,
			wantPrefix: "task/" + taskID + "-",
		},
		{
			name: "a title nobody asked the model for keeps the id", title: "Fix login",
			wantPrefix: "task/" + taskID + "-", wantLen: len("task/"+taskID) + 1 + randomSuffixLen,
		},
		{
			name: "a title that slugs to nothing keeps the id", title: "###", titleLLM: true,
			wantPrefix: "task/" + taskID + "-", wantLen: len("task/"+taskID) + 1 + randomSuffixLen,
		},
		{
			name: "a long title is cut to the cap", title: longTitle, titleLLM: true,
			maxLen: types.MaxBranchName,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, _, projectID, _ := setupExecutorWithGit(t)
			task := &types.Task{ID: taskID, ProjectID: projectID, Title: tc.title, TitleLLM: tc.titleLLM, AutoMerge: tc.autoMerge}
			branchName := te.merge.generateBranchName(task, projectID)
			if !strings.HasPrefix(branchName, tc.wantPrefix) {
				t.Errorf("branch name = %q, want prefix %q", branchName, tc.wantPrefix)
			}
			if tc.wantLen != 0 && len(branchName) != tc.wantLen {
				t.Errorf("branch name len = %d (%q), want %d", len(branchName), branchName, tc.wantLen)
			}
			if tc.maxLen != 0 && len(branchName) > tc.maxLen {
				t.Errorf("branch name len = %d (%q), want <= %d", len(branchName), branchName, tc.maxLen)
			}
		})
	}
}

// TestPersistAgentTurnEntries_SandboxField verifies that the sandbox field
// is injected into stored ToolCallsJSON for tool_call entries, so the frontend
// can display the correct sandbox icon after page refresh.
func TestPersistAgentTurnEntries_SandboxField(t *testing.T) {
	t.Parallel()
	e, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-sandbox-persist"
	threadID := "test-thread"

	// Create the task in the DB
	task := &types.Task{ID: taskID, ProjectID: projectID, CurrentThreadID: threadID, Branch: "main"}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	testPayload := pkgagentPersistEntry(
		"read_file",
		`{"path":"README.md"}`,
	)

	// Persist with sandbox=true
	e.persistAgentTurnEntries(projectID, taskID, threadID, []pkgagent.PersistEntry{
		{Role: "tool_call", ToolCallsJSON: testPayload},
	}, time.Now(), true)

	msgs, err := e.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var toolCallMsg *types.TaskMessage
	for i := range msgs {
		if msgs[i].Role == "tool_call" {
			toolCallMsg = &msgs[i]
			break
		}
	}
	if toolCallMsg == nil {
		t.Fatal("expected a tool_call message")
	}

	// Verify sandbox field is present and true
	var tcData map[string]any
	if err := json.Unmarshal([]byte(toolCallMsg.ToolCalls), &tcData); err != nil {
		t.Fatalf("unmarshal tool calls: %v", err)
	}
	if sandbox, ok := tcData["sandbox"].(bool); !ok || !sandbox {
		t.Errorf("expected sandbox=true in stored ToolCallsJSON, got: %v", tcData["sandbox"])
	}
	if tcData["tool_name"] != "read_file" {
		t.Errorf("expected tool_name=read_file, got: %v", tcData["tool_name"])
	}

	// Now persist with sandbox=false
	e.persistAgentTurnEntries(projectID, taskID, threadID, []pkgagent.PersistEntry{
		{Role: "tool_call", ToolCallsJSON: pkgagentPersistEntry("bash", `{"command":"ls"}`)},
	}, time.Now(), false)

	msgs2, err := e.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var bashMsg *types.TaskMessage
	for i := range msgs2 {
		if msgs2[i].Role == "tool_call" && msgs2[i].ToolCalls != "" {
			var d map[string]any
			if json.Unmarshal([]byte(msgs2[i].ToolCalls), &d) == nil && d["tool_name"] == "bash" {
				bashMsg = &msgs2[i]
				break
			}
		}
	}
	if bashMsg == nil {
		t.Fatal("expected a bash tool_call message")
	}
	var bashData map[string]any
	if err := json.Unmarshal([]byte(bashMsg.ToolCalls), &bashData); err != nil {
		t.Fatalf("unmarshal bash tool calls: %v", err)
	}
	if sandbox, ok := bashData["sandbox"].(bool); !ok || sandbox {
		t.Errorf("expected sandbox=false in stored ToolCallsJSON, got: %v", bashData["sandbox"])
	}
}

// pkgagentPersistEntry builds a ToolCallsJSON string for testing.
func pkgagentPersistEntry(toolName, argsJSON string) string {
	data := map[string]any{
		"tool_id":           "call-test",
		"tool_name":         toolName,
		"tool_display_name": toolName,
		"args":              nil,
		"raw_args":          argsJSON,
	}
	b, _ := json.Marshal(data)
	return string(b)
}

func TestForwardUserMessage_BlocksLazyStartDuringSummarize(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.summarize.blockedMessages.tasks = make(map[string]struct{})
	taskID := "test-task"

	// Create a task with summarizing flag set
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Prompt:         "Test task",
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
		LlamaServerURL: "http://localhost:8080/v1",
		ModelName:      "test-model",
		Summarizing:    true,
	}
	te.store.CreateTask(task)

	// Forward a user message — should NOT trigger lazy-start
	te.ForwardUserMessage(projectID, taskID, stream.UserMessage{
		Content:   "Hello",
		ModelName: "test-model",
		ServerURL: "http://localhost:8080/v1",
	})

	// Give a brief moment for any goroutine to start
	time.Sleep(50 * time.Millisecond)

	// Verify no executor was created (lazy-start was blocked)
	te.taskMu.Lock()
	_, exists := te.tasks[taskID]
	te.taskMu.Unlock()
	if exists {
		t.Fatal("executor should not be created during active summarization")
	}

	// Verify the blocked message was tracked
	te.summarize.blockedMessages.mu.Lock()
	_, hasBlocked := te.summarize.blockedMessages.tasks[taskID]
	te.summarize.blockedMessages.mu.Unlock()
	if !hasBlocked {
		t.Fatal("blocked message should be tracked")
	}
}

// TestInitTask_ClearsOrphanedBranchOnWorktreeFailure verifies that when
// CreateWorktree fails after SetTaskBranchIfEmpty, the branch name is
// cleared from the DB so it doesn't become an orphaned ref.
func TestInitTask_ClearsOrphanedBranchOnWorktreeFailure(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-orphaned-branch-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    types.StatusBacklog,
	}
	te.store.CreateTask(task)

	// Corrupt the git repo so CreateWorktree will fail.
	// SetTaskBranchIfEmpty runs first (DB-only), then CreateWorktree needs git.
	os.RemoveAll(filepath.Join(repoDir, ".git"))

	_, err := te.InitTask(projectID, taskID)
	if err == nil {
		t.Fatal("InitTask should fail when git repo is corrupted")
	}

	// Branch should be cleared (not orphaned in DB)
	reloaded, _ := te.store.GetTaskByID(taskID)
	if reloaded.Branch != "" {
		t.Fatalf("branch should be cleared after worktree failure, got %q", reloaded.Branch)
	}
}

// A panicking tool must still produce a result and must not strand the worktree
// lock: a tool_call left without a result renders as "Running" forever, and the
// stranded lock deadlocks every later write for the task.
func TestRunToolProtected_PanicBecomesFailedResult(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)

	result, ok := te.runToolProtected("task-panic", "bash", func() (string, bool) {
		panic("boom")
	})

	if ok {
		t.Errorf("success = true after a panic, want false")
	}
	if !strings.Contains(result, "internal error while running bash") || !strings.Contains(result, "boom") {
		t.Errorf("result = %q, want an internal-error message naming the tool", result)
	}

	// The reason the panic guard exists alongside the lock: a stranded worktree lock deadlocks every
	// later write for the task, in this process, until restart.
	if taskLockIsHeld(te, "task-panic") {
		t.Fatal("worktree lock still held after a panicking tool — later tools would deadlock")
	}
}

func TestRunToolProtected_PassesResultThroughUnderLock(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)

	var heldInside bool
	result, ok := te.runToolProtected("task-ok", "read_file", func() (string, bool) {
		// Same goroutine, and the lock is a semaphore rather than an owner-checked mutex, so this
		// reports "held" by failing to take it a second time — which is exactly the property: one tool
		// call at a time per worktree.
		heldInside = taskLockIsHeld(te, "task-ok")
		return "file contents", true
	})

	if !ok || result != "file contents" {
		t.Errorf("runToolProtected() = (%q, %v), want (\"file contents\", true)", result, ok)
	}
	if !heldInside {
		t.Error("the worktree lock was not held while the tool ran")
	}
	if taskLockIsHeld(te, "task-ok") {
		t.Fatal("worktree lock still held after a successful tool")
	}
}

// TestPrepareTaskExecutionRefusesARunningTaskWithTheSentinel: a second start of a task this process is
// already running is the caller's own doing, not a server fault, so it has to arrive as the sentinel the
// transport maps to 409. It was returned as a bare fmt.Errorf("task already running"), which answered 500
// and left the handler's errors.Is branch — and its 409 — unreachable.
func TestPrepareTaskExecutionRefusesARunningTaskWithTheSentinel(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "task-already-running"
	if err := te.store.CreateTask(&types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Already running",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, executorType: "llm"}
	t.Cleanup(func() { cancel() })

	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if setup != nil {
		t.Error("a task that is actively running was prepared anyway")
	}
	if !errors.Is(err, ErrTaskAlreadyRunning) {
		t.Errorf("err = %v, want it to wrap ErrTaskAlreadyRunning (the API maps that to 409)", err)
	}
}

// TestErrTaskAlreadyRunningClassifiesAsConflict holds the sentinel to what its comment claims: the
// transport answers 409 because the error declares a conflict, not because a handler recognised its text.
func TestErrTaskAlreadyRunningClassifiesAsConflict(t *testing.T) {
	t.Parallel()
	if !errs.IsConflict(ErrTaskAlreadyRunning) {
		t.Error("ErrTaskAlreadyRunning does not classify as errs.ErrConflict, so the API answers 500")
	}
	if got := ErrTaskAlreadyRunning.Error(); got != "task already running" {
		t.Errorf("message = %q, want %q — it is the browser's text", got, "task already running")
	}
}
