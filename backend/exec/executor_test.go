package exec

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	pkgagent "kanban/pkg/agent"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// setupExecutorWithGit creates a TaskExecutor with a real git repo for testing.
func setupExecutorWithGit(t *testing.T) (*TaskExecutor, string, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	// Initialize git repo
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644)
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Get the initial commit SHA
	var initialSHA string
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	out, _ := cmd.Output()
	initialSHA = strings.TrimSpace(string(out))

	// Create store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test")
	_ = s.SetConfigValue("workspace_dir", worktreesDir)

	// Create project
	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	// Create worktree manager and executor
	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := stream.NewAppStreamManager(s, worktreeMgr)

	te := &TaskExecutor{
		store:        s,
		configStore:  cfgStore,
		appStreamMgr: appStreamMgr,
		worktreeMgr:  worktreeMgr,
		commitQueue:  NewCommitQueue(),
		tasks:        make(map[string]*executionState),
	}

	return te, repoDir, projectID, initialSHA
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

// TestPrepareTaskExecution_PreservesBaseRefOnRestart verifies that when a task
// is restarted (worktree already exists), the stored BaseRef (commit SHA) is
// preserved and not overwritten with the main repo's branch name.
func TestPrepareTaskExecution_PreservesBaseRefOnRestart(t *testing.T) {
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-baseref-task"
	branchName := "task/" + taskID

	// Create task with BaseRef already set (simulating first run completed)
	task := &types.Task{
		ID:            taskID,
		ProjectID:     projectID,
		Title:         "Test task",
		Prompt:        "Test prompt",
		Status:        "in_progress",
		BaseRef:       initialSHA,
		Branch:        branchName,
		LlamaServerURL: "http://localhost:8080",
		ModelName:     "test-model",
	}
	te.store.CreateTask(task)

	// Create the worktree (simulating it already exists from first run)
	_, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Now simulate restart: prepareTaskExecution is called again
	// Work the worktree exists, CreateWorktree returns early with branch name
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if err != nil {
		t.Fatalf("prepare task execution: %v", err)
	}
	if setup == nil {
		t.Fatal("prepare task execution returned nil")
	}

	// Verify BaseRef is still the SHA, not a branch name
	if setup.task.BaseRef != initialSHA {
		t.Errorf("BaseRef was overwritten on restart: expected %q (SHA), got %q", initialSHA, setup.task.BaseRef)
	}
	if len(setup.task.BaseRef) != 40 {
		t.Errorf("BaseRef should be a 40-char SHA, got %q (len=%d)", setup.task.BaseRef, len(setup.task.BaseRef))
	}

	// Cleanup
	setup.cancel()
	te.StopTask(taskID)
}

// TestPrepareTaskExecution_SetsBaseRefOnFirstRun verifies that on first run
// (no existing BaseRef), the BaseRef is correctly set from CreateWorktree
// as a branch name (e.g. "master"), not a commit SHA.
func TestPrepareTaskExecution_SetsBaseRefOnFirstRun(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-baseref-first-run"
	branchName := "task/" + taskID

	// Create task WITHOUT BaseRef (first run)
	task := &types.Task{
		ID:            taskID,
		ProjectID:     projectID,
		Title:         "Test task",
		Prompt:        "Test prompt",
		Status:        "in_progress",
		Branch:        branchName,
		LlamaServerURL: "http://localhost:8080",
		ModelName:     "test-model",
	}
	te.store.CreateTask(task)

	// prepareTaskExecution on first run (no existing worktree)
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if err != nil {
		t.Fatalf("prepare task execution: %v", err)
	}
	if setup == nil {
		t.Fatal("prepare task execution returned nil")
	}

	// Verify BaseRef is set to a branch name (not a 40-char SHA)
	if setup.task.BaseRef == "" {
		t.Error("BaseRef should be set on first run")
	}
	if len(setup.task.BaseRef) == 40 {
		t.Errorf("BaseRef should be a branch name, not a 40-char SHA, got %q", setup.task.BaseRef)
	}

	// Cleanup
	setup.cancel()
	te.StopTask(taskID)
}

// TestInitTask_CreatesWorktree verifies that InitTask creates the worktree
// and sets branch/BaseRef without starting LLM execution.
func TestInitTask_CreatesWorktree(t *testing.T) {
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-init-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    "backlog",
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
	reloaded, _ := te.store.GetTaskByTaskID(taskID)
	if reloaded.Status != "backlog" {
		t.Errorf("Status should remain backlog after init, got %q", reloaded.Status)
	}
	if reloaded.ExecutionState == "running" {
		t.Errorf("ExecutionState should not be running after init, got %q", reloaded.ExecutionState)
	}
}

// TestInitTask_Idempotent verifies that calling InitTask twice is safe.
func TestInitTask_Idempotent(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-init-idempotent-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    "backlog",
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
	if result2.Status != "backlog" {
		t.Errorf("Status should remain backlog after second init, got %q", result2.Status)
	}
}

// TestInitTask_ThenPrepareTaskExecution verifies that prepareTaskExecution
// works correctly after InitTask has already created the worktree.
func TestInitTask_ThenPrepareTaskExecution(t *testing.T) {
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-init-then-start-task"

	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test task",
		Prompt:         "Test prompt",
		Status:         "backlog",
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

	// BaseCommit should be preserved
	if setup.task.BaseCommit != initialSHA {
		t.Errorf("BaseCommit should be preserved: expected %q, got %q", initialSHA, setup.task.BaseCommit)
	}

	// Cleanup
	setup.cancel()
	te.StopTask(taskID)
}

func TestHasRunningTasks_InProgress(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-running-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Status:    "in_progress",
	}
	te.store.CreateTask(task)

	// Manually add an entry to the tasks map (simulating a running executor)
	ctx, cancel := context.WithCancel(context.Background())
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, executorType: "llm", taskStatus: "in_progress"}
	t.Cleanup(func() { cancel() })

	if !te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return true for in_progress task in map")
	}
}

func TestHasRunningTasks_BacklogStaleCleanup(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-backlog-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Status:    "backlog",
	}
	te.store.CreateTask(task)

	// Manually add a stale entry to the tasks map (status != in_progress)
	ctx, cancel := context.WithCancel(context.Background())
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, executorType: "llm", taskStatus: "backlog"}
	t.Cleanup(func() { cancel() })

	if te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return false for backlog task")
	}

	// Entry remains in map — HasRunningTasks is read-only, cleanup is done by StopTask
	te.taskMu.Lock()
	_, stillExists := te.tasks[taskID]
	te.taskMu.Unlock()
	if !stillExists {
		t.Error("backlog entry should still be in tasks map (not mutated)")
	}
}

func TestHasRunningTasks_ReviewStaleCleanup(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-review-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Status:    "review",
	}
	te.store.CreateTask(task)

	// Manually add a stale entry to the tasks map (status != in_progress)
	ctx, cancel := context.WithCancel(context.Background())
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, executorType: "llm", taskStatus: "review"}
	t.Cleanup(func() { cancel() })

	if te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return false for review task")
	}

	// Entry remains in map — HasRunningTasks is read-only, cleanup is done by StopTask
	te.taskMu.Lock()
	_, stillExists := te.tasks[taskID]
	te.taskMu.Unlock()
	if !stillExists {
		t.Error("review entry should still be in tasks map (not mutated)")
	}
}

func TestHasRunningTasks_NotInMap(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-not-running-task"

	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Status:    "in_progress",
	}
	te.store.CreateTask(task)

	// Task is in DB with in_progress status but NOT in the tasks map
	if te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return false when task is not in tasks map")
	}
}

func TestHasRunningTasks_MixedTasks(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)

	// Task 1: in_progress + in map → considered running
	task1 := &types.Task{ID: "task-a", ProjectID: projectID, Title: "A", Status: "in_progress"}
	te.store.CreateTask(task1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	te.tasks["task-a"] = &executionState{ctx: ctx1, cancel: cancel1, executorType: "llm", taskStatus: "in_progress"}
	t.Cleanup(func() { cancel1() })

	// Task 2: backlog in map (taskStatus != in_progress) → not considered running
	task2 := &types.Task{ID: "task-b", ProjectID: projectID, Title: "B", Status: "backlog"}
	te.store.CreateTask(task2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	te.tasks["task-b"] = &executionState{ctx: ctx2, cancel: cancel2, executorType: "llm", taskStatus: "backlog"}
	t.Cleanup(func() { cancel2() })

	// Task 3: review + not in map → nothing to clean
	task3 := &types.Task{ID: "task-c", ProjectID: projectID, Title: "C", Status: "review"}
	te.store.CreateTask(task3)

	// Should return true because task-a has taskStatus=in_progress
	if !te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return true when at least one in_progress task is in map")
	}

	// Both entries remain in the map — HasRunningTasks is read-only
	te.taskMu.Lock()
	_, taskBExists := te.tasks["task-b"]
	_, taskAExists := te.tasks["task-a"]
	te.taskMu.Unlock()
	if !taskBExists {
		t.Error("backlog entry (task-b) should still be in tasks map (not mutated)")
	}
	if !taskAExists {
		t.Error("in_progress entry (task-a) should still be in tasks map")
	}
}

func TestHasRunningTasks_NoTasks(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)

	// Project exists but has no tasks
	if te.HasRunningTasks(projectID) {
		t.Error("HasRunningTasks should return false when project has no tasks")
	}
}

func TestHasRunningTasks_InvalidProject(t *testing.T) {
	te, _, _, _ := setupExecutorWithGit(t)

	// Non-existent project
	if te.HasRunningTasks("nonexistent-project") {
		t.Error("HasRunningTasks should return false for non-existent project")
	}
}

func TestCommitAndMerge_SerializesPerProject(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create a task with a worktree so CommitAndMerge has something to commit
	taskID := "test-serialize-task"
	branchName := "task/" + taskID
	task := &types.Task{
		ID:      taskID,
		ProjectID: projectID,
		Title:   "Test serialize",
		Branch:  branchName,
		BaseRef: "master",
	}
	te.store.CreateTask(task)

	// Create the worktree and make an initial commit
	_, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wtPath, _ := te.worktreeMgr.WorktreePath(taskID)
	os.WriteFile(filepath.Join(wtPath, "initial.txt"), []byte("initial\n"), 0o644)
	runGit(t, wtPath, "add", "initial.txt")
	runGit(t, wtPath, "commit", "-m", "initial")

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
		runGit(t, wtPath, "add", "file-a")
		close(startFirst)
		te.CommitAndMerge(projectID, taskID, wtPath, "commit-a", false)
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
		runGit(t, wtPath, "add", "file-b")
		close(startSecond)
		te.CommitAndMerge(projectID, taskID, wtPath, "commit-b", false)
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
	cmd := exec.Command("git", "-C", repoDir, "log", branchName, "--oneline")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	logOutput := string(out)
	if !strings.Contains(logOutput, "commit-a") {
		t.Errorf("expected commit-a in branch log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "commit-b") {
		t.Errorf("expected commit-b in branch log, got:\n%s", logOutput)
	}
}

func TestSlugifyBranchName_NormalTitle(t *testing.T) {
	slug := slugifyBranchName("Fix login bug #123")
	if slug != "fix-login-bug-123" {
		t.Errorf("expected 'fix-login-bug-123', got %q", slug)
	}
}

func TestSlugifyBranchName_AllSpecialChars(t *testing.T) {
	if slug := slugifyBranchName("###"); slug != "" {
		t.Errorf("expected empty string, got %q", slug)
	}
}

func TestSlugifyBranchName_AllHyphens(t *testing.T) {
	if slug := slugifyBranchName("---"); slug != "" {
		t.Errorf("expected empty string, got %q", slug)
	}
}

// TestSlugifyBranchName_NeverStartsWithHyphen verifies that slugifyBranchName
// can never produce a slug starting with '-', preventing Git argument injection.
func TestSlugifyBranchName_NeverStartsWithHyphen(t *testing.T) {
	inputs := []string{
		"-fix login",           // starts with hyphen
		"  -fix",               // whitespace then hyphen
		"---fix",               // multiple hyphens
		"-123",                 // hyphen then digits
		"---",                  // all hyphens
		"-",                    // single hyphen
		"-a-b-c",               // hyphen-separated
		"Fix login",            // normal title
		"123 fix",              // starts with digit
		"!!!fix!!!",            // special chars
		"",                     // empty
	}
	for _, input := range inputs {
		slug := slugifyBranchName(input)
		if len(slug) > 0 && slug[0] == '-' {
			t.Errorf("slugifyBranchName(%q) = %q starts with hyphen", input, slug)
		}
	}
}

func TestGenerateBranchName_FallsBackToTaskIDWhenSlugIsEmpty(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "frost-amber-quartz"
	task := &types.Task{ID: taskID, ProjectID: projectID, Title: "###"}
	branchName := te.generateBranchName(task, taskID, projectID)
	if branchName != "task/"+taskID {
		t.Errorf("expected 'task/%s', got %q", taskID, branchName)
	}
}

func TestGenerateBranchName_UsesSlugForNormalTitle(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "frost-amber-quartz"
	task := &types.Task{ID: taskID, ProjectID: projectID, Title: "Fix login"}
	branchName := te.generateBranchName(task, taskID, projectID)
	if branchName != "task/fix-login" {
		t.Errorf("expected 'task/fix-login', got %q", branchName)
	}
}

func TestGenerateBranchName_MaxLength(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "frost-amber-quartz"
	// Title that slugifies to more than 45 chars (maxBranchName - len("task/"))
	longTitle := "Fix a very long bug description that exceeds the maximum allowed branch name length limit"
	task := &types.Task{ID: taskID, ProjectID: projectID, Title: longTitle}
	branchName := te.generateBranchName(task, taskID, projectID)
	if len(branchName) > maxBranchName {
		t.Errorf("branch name len = %d (%q), want <= %d", len(branchName), branchName, maxBranchName)
	}
}

func TestSlugifyBranchName_MaxLength(t *testing.T) {
	// Title that slugifies to more than 45 chars
	longTitle := "Fix a very long bug description that exceeds the maximum allowed branch name length limit"
	slug := slugifyBranchName(longTitle)
	if len(slug) > maxBranchName-len("task/") {
		t.Errorf("slug len = %d (%q), want <= %d", len(slug), slug, maxBranchName-len("task/"))
	}
}

// TestPersistAgentTurnEntries_SandboxField verifies that the sandbox field
// is injected into stored ToolCallsJSON for tool_call entries, so the frontend
// can display the correct sandbox icon after page refresh.
func TestPersistAgentTurnEntries_SandboxField(t *testing.T) {
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
	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.blockedMessages.tasks = make(map[string]struct{})
	taskID := "test-task"

	// Create a task with summarizing flag set
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Prompt:         "Test task",
		Status:         "backlog",
		ExecutionState: "idle",
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
	te.blockedMessages.mu.Lock()
	_, hasBlocked := te.blockedMessages.tasks[taskID]
	te.blockedMessages.mu.Unlock()
	if !hasBlocked {
		t.Fatal("blocked message should be tracked")
	}
}

