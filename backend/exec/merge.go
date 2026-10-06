package exec

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"
	"mike/pkg/logging"
)

// mergeOrchestrator owns what happens to a task's commits once a turn ends: branch, merge,
// commit message, and the commit of a finished conflict resolution.
//
// It holds no executionState and takes neither taskMu nor sandboxMu. Serialization comes from
// lockWorktree and from the release funcs WorktreeManager owns. Task state is read from the store,
// so a merge can run while no executor holds the task.
type mergeOrchestrator struct {
	store        *store.Store
	worktreeMgr  *git.WorktreeManager
	appStreamMgr *stream.AppStreamManager
	commitQueue  *CommitQueue
	// A release func, not a mutex, so the lock cannot leave the package that owns it.
	lockWorktree func(taskID string) func()
	storeTask    func(*types.Task, string) error
	// Diff stats are the watcher's concern, not a merge's.
	broadcastDiffStats func(projectID, taskID string)
	// Injected so a commit message and a chat turn cannot disagree about which provider answers.
	utilityTarget    func(task *types.Task) (serverURL, model string, ok bool)
	chatCompletion   func(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, opts *ChatCompletionOpts) (*types.ChatUsage, string, error)
	streamCompletion func(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, cb types.StreamCallback, onProgress types.PromptProgressCallback) (*types.ChatUsage, error)
	// Tasks whose merge conflict came from a commit-and-done click. In memory because only the
	// click knows it was a click; a conflict found at task start means "keep working".
	commitDone struct {
		mu    sync.Mutex
		tasks map[string]struct{}
	}
}

// One wiring definition, so every executor's merge collaborator is wired the same way.
func (te *TaskExecutor) newMergeOrchestrator() *mergeOrchestrator {
	m := &mergeOrchestrator{
		store:              te.store,
		worktreeMgr:        te.worktreeMgr,
		appStreamMgr:       te.appStreamMgr,
		commitQueue:        te.commitQueue,
		lockWorktree:       te.LockWorktree,
		storeTask:          te.storeTask,
		broadcastDiffStats: te.broadcastParentDiffStats,
		utilityTarget:      te.utilityTarget,
		chatCompletion:     te.ChatCompletion,
		streamCompletion:   te.StreamCompletion,
	}
	m.commitDone.tasks = make(map[string]struct{})
	return m
}

func (te *TaskExecutor) MergeBranch(projectID, taskID string, task *types.Task) error {
	return te.merge.MergeBranch(projectID, taskID, task)
}

func (te *TaskExecutor) ResolveMergeTarget(projectID string, task *types.Task) string {
	return te.merge.ResolveMergeTarget(projectID, task)
}

func (te *TaskExecutor) EnsureParentBranch(projectID string, parent *types.Task) error {
	return te.merge.EnsureParentBranch(projectID, parent)
}

func (te *TaskExecutor) ArmCommitAndDone(taskID string) {
	te.merge.ArmCommitAndDone(taskID)
}

// CommitAndDonePending reads the armed click back without consuming it. The merge path consults the
// orchestrator itself; this forward is how backend/api's tests watch the arming rule, which lives
// behind the unexported te.merge.
func (te *TaskExecutor) CommitAndDonePending(taskID string) bool {
	return te.merge.CommitAndDonePending(taskID)
}

func (te *TaskExecutor) GenerateCommitMessage(ctx context.Context, task *types.Task) string {
	return te.merge.GenerateCommitMessage(ctx, task)
}

func (te *TaskExecutor) StreamCommitMessage(s *httputil.SSE, taskID string, task *types.Task) {
	te.merge.StreamCommitMessage(s, taskID, task)
}

// branchChildFrom settles the branch a task forks from. "" means the project baseline.
// The parent's real worktree branch is used, not the stored parent.Branch, which can be stale.
func (m *mergeOrchestrator) branchChildFrom(projectID string, task *types.Task) (string, error) {
	if task.ParentTaskID == "" {
		return "", nil
	}

	parent, err := m.store.GetTaskByID(task.ParentTaskID)
	// A parent whose branch was merged and cleaned up gets it back from HEAD here, so the child
	// has a real source.
	if err == nil {
		if err := m.EnsureParentBranch(projectID, parent); err != nil {
			return "", fmt.Errorf("ensure parent branch: %w", err)
		}
	}

	// Re-read: EnsureParentBranch, or a caller that won the same race, may have set it.
	parent, err = m.store.GetTaskByID(task.ParentTaskID)
	if err != nil {
		return "", nil
	}

	if parentWtPath, wtErr := m.worktreeMgr.WorktreePath(parent.ID); wtErr == nil {
		if _, statErr := os.Stat(parentWtPath); statErr == nil {
			if actualBranch, gitErr := m.worktreeMgr.GitRunInDirOutput(parentWtPath, "rev-parse", "--abbrev-ref", "HEAD"); gitErr == nil {
				if b := strings.TrimSpace(actualBranch); b != "" {
					return b, nil
				}
			}
		}
	}
	return parent.Branch, nil
}

// EnsureParentBranch gives a not-yet-started parent (parent.Branch == "") the branch that becomes
// the subtask auto-merge target. It creates a full worktree, not a bare branch, so
// prepareTaskExecution reuses it when the parent starts. Only Branch is written: the parent keeps
// its status and column while subtasks run.
func (m *mergeOrchestrator) EnsureParentBranch(projectID string, parent *types.Task) error {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	if parent.Branch != "" {
		if m.branchExists(projectID, parent.Branch) {
			return nil
		}
		// Create it from HEAD so the stored name becomes valid again.
		cmd := git.Command("", "-C", project.Path, "branch", parent.Branch, "HEAD")
		if out, err := cmd.CombinedOutput(); err != nil {
			logging.For(logging.Merge).With(logging.TaskID, parent.ID).Warnf("branch %q missing in git, create failed: %s", parent.Branch, string(out))
			return fmt.Errorf("create missing parent branch %q: %w", parent.Branch, err)
		}
		logging.For(logging.Merge).With(logging.TaskID, parent.ID).Warnf("recovered missing branch %q from HEAD", parent.Branch)
		return nil
	}

	branchName := m.generateBranchName(parent, parent.ID, projectID)

	// The compare-and-set stops two callers creating a worktree for the same parent.
	set, err := m.store.SetTaskBranchIfEmpty(parent.ID, projectID, branchName)
	if err != nil {
		return fmt.Errorf("set parent branch: %w", err)
	}
	if !set {
		latest, err := m.store.GetTaskByID(parent.ID)
		if err != nil {
			return fmt.Errorf("reload parent task: %w", err)
		}
		if latest.Branch != "" {
			parent.Branch = latest.Branch
			return nil
		}
		// Unreachable safety net — SQLite guarantees this won't happen.
	}

	// CreateWorktree makes the branch and its directory; when the parent starts later it finds
	// the directory and reuses it.
	_, _, err = m.worktreeMgr.CreateWorktree(projectID, parent.ID, branchName, "")
	if err != nil {
		return fmt.Errorf("create parent worktree: %w", err)
	}

	// Refresh updatedAt; adopt another caller's branch name if they won the race.
	latest, _ := m.store.GetTaskByID(parent.ID)
	if latest.Branch != "" && latest.Branch != branchName {
		branchName = latest.Branch
	}
	parent.Branch = branchName
	parent.UpdatedAt = time.Now().UTC()
	if err := m.storeTask(parent, projectID); err != nil {
		_ = m.worktreeMgr.RemoveWorktree(projectID, parent.ID, branchName)
		return fmt.Errorf("update parent task with branch: %w", err)
	}

	logging.For(logging.Merge).With(logging.TaskID, parent.ID).Infof("created worktree with branch %s for parent", branchName)
	return nil
}

func (m *mergeOrchestrator) branchExists(projectID, branchName string) bool {
	_, err := m.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--verify", branchName)
	return err == nil
}

// recoverMissingBranch returns a stale branch sharing the missing branch's slug, recording it on
// the task, or "" when there is no match.
func (m *mergeOrchestrator) recoverMissingBranch(projectID, taskID, missingBranch string) string {
	slugPrefix := types.TaskBranchSlugPrefix(missingBranch)
	if slugPrefix == "" {
		return ""
	}

	branches, err := m.worktreeMgr.ListProjectBranches(projectID, slugPrefix)
	if err != nil {
		return ""
	}

	var matches []string
	for _, b := range branches {
		if b != missingBranch {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return ""
	}

	// If multiple matches, pick the most recent one (by commit date).
	var recovered string
	if len(matches) == 1 {
		recovered = matches[0]
	} else {
		// Pick the branch with the most recent commit.
		var latestTime time.Time
		for _, b := range matches {
			tsOut, err := m.worktreeMgr.GitRunInProjectOutput(projectID, "log", "-1", "--format=%ct", b)
			if err != nil {
				continue
			}
			if ts, err := strconv.ParseInt(strings.TrimSpace(tsOut), 10, 64); err == nil {
				t := time.Unix(ts, 0)
				if t.After(latestTime) {
					latestTime = t
					recovered = b
				}
			}
		}
		if recovered == "" {
			recovered = matches[0]
		}
	}

	if err := m.store.SetTaskBranch(taskID, projectID, recovered); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to update branch from %q to %q: %v", missingBranch, recovered, err)
		return ""
	}
	logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("recovered missing branch %q → %q", missingBranch, recovered)
	return recovered
}

// randomSuffixLen is the random tail of a task branch name. types.TaskBranchSuffixLen is the
// matching half of the branch-name parser — the two must agree.
const randomSuffixLen = types.TaskBranchSuffixLen

func randomSuffix(n int) string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// generateBranchName builds the task's branch name: the slugified title when there is one, the
// task ID otherwise, always with a random tail because a branch left behind by a deleted task
// would otherwise be reused. The whole name stays within types.MaxBranchName.
func (m *mergeOrchestrator) generateBranchName(task *types.Task, taskID, projectID string) string {
	baseName := types.TaskBranchPrefix + task.ID
	if !task.AutoMerge && task.Title != "" {
		slug := types.SlugifyBranchName(task.Title)
		if slug != "" {
			baseName = types.TaskBranchPrefix + slug
		}
	}
	suffixSpace := 1 + randomSuffixLen
	if len(baseName)+suffixSpace > types.MaxBranchName {
		baseName = baseName[:types.MaxBranchName-suffixSpace]
	}
	branchName := baseName + "-" + randomSuffix(randomSuffixLen)

	wtPath, wtErr := m.worktreeMgr.WorktreePath(taskID)
	_, wtStatErr := os.Stat(wtPath)
	wtExists := wtErr == nil && wtStatErr == nil
	if !wtExists && m.branchExists(projectID, branchName) {
		extra := "-" + taskID[:8]
		if len(branchName)+len(extra) <= types.MaxBranchName {
			branchName += extra
		}
	}
	return branchName
}

// logSystemMessage adds a note about a merge to the task's thread. An empty severity renders as the
// default warning style.
func (m *mergeOrchestrator) logSystemMessage(taskID, threadID, severity, content string) {
	systemNote{severity: severity, content: content}.recordOnThread(m.store, taskID, threadID)
}

// ResolveMergeTarget answers where the task's work lands: a child merges into its parent's branch,
// a root task into base_ref or the project's origin branch.
func (m *mergeOrchestrator) ResolveMergeTarget(projectID string, task *types.Task) string {
	if task.ParentTaskID != "" {
		// mergeInto creates the parent's branch on demand, so the child's own base_ref is never its target.
		if parent, err := m.store.GetTaskByID(task.ParentTaskID); err == nil && parent.Branch != "" {
			return parent.Branch
		}
	} else if task.BaseRef != "" {
		return task.BaseRef
	}
	project, err := m.store.GetProject(projectID)
	if err != nil || project.OriginBranch == "" {
		return ""
	}
	return project.OriginBranch
}

// GenerateCommitMessage writes the task's commit message from the conversation.
// Every commit path goes through it, and each model that contributed gets its own Assisted-by line
// unless the project turned that line off.
func (m *mergeOrchestrator) GenerateCommitMessage(ctx context.Context, task *types.Task) string {
	assistedBy := m.assistedByEnabled(task.ProjectID)
	taskMsgs, _ := m.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	conversation := BuildConversationFromTaskMessages(taskMsgs)
	commitMsg := m.generateCommitMessageLLM(ctx, task, commitContextText(conversation), assistedBy)
	// The LLM's generic answer when it could not run; the title says more.
	if commitMsg == "file changes" && task.Title != "" {
		commitMsg = task.Title
	}

	var models []string
	if assistedBy {
		models = collectUniqueModels(taskMsgs)
		if len(models) == 0 {
			if task.ModelName != "" {
				models = []string{task.ModelName}
			} else {
				models = []string{"unknown model"}
			}
		}
	}

	return buildCommitMessageWithAssistedBy(commitMsg, models)
}

// assistedByEnabled reads the project's answer to the Assisted-by trailer; a project that cannot be
// read keeps it.
func (m *mergeOrchestrator) assistedByEnabled(projectID string) bool {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return true
	}
	return project.AssistedByEnabled()
}

// generateCommitMessageLLM asks the model for the message, falling back to the task's title (plus the
// Assisted-by line when the project wants it) on any error.
func (m *mergeOrchestrator) generateCommitMessageLLM(ctx context.Context, task *types.Task, turnText string, assistedBy bool) string {
	serverURL, modelName, _ := m.utilityTarget(task)
	if serverURL == "" || modelName == "" {
		return "file changes"
	}
	fallback := task.Title
	if assistedBy {
		fallback = buildCommitMessageWithAssistedBy(task.Title, []string{modelName})
	}

	labelMessages := buildCommitMessages(turnText)

	maxTokens := 128
	_, label, err := m.chatCompletion(ctx, serverURL, modelName, labelMessages, &ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
	if err != nil {
		logging.For(logging.Generate).With(logging.TaskID, task.ID).Warnf("LLM call failed: %v", err)
		return fallback
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fallback
	}
	logging.For(logging.Generate).With(logging.TaskID, task.ID).Infof("generated: %q", label)
	return label
}

// StreamCommitMessage streams the message as SSE events. No timeout is applied.
func (m *mergeOrchestrator) StreamCommitMessage(s *httputil.SSE, taskID string, task *types.Task) {
	serverURL, modelName, _ := m.utilityTarget(task)
	if serverURL == "" || modelName == "" {
		fallback := task.Title
		if fallback == "" {
			fallback = "file changes"
		}
		_ = s.Event("done", fmt.Sprintf("{\"content\":%q}", fallback))
		return
	}

	taskMsgs, _ := m.store.GetTaskMessages(taskID, task.CurrentThreadID)
	conversation := BuildConversationFromTaskMessages(taskMsgs)
	turnText := commitContextText(conversation)
	labelMessages := buildCommitMessages(turnText)

	var models []string
	if m.assistedByEnabled(task.ProjectID) {
		models = collectUniqueModels(taskMsgs)
		if len(models) == 0 {
			models = []string{task.ModelName}
		}
	}

	var sb strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeSSE := func(event, data string) {
		if err := s.Event(event, data); err != nil {
			cancel()
		}
	}

	_, err := m.streamCompletion(ctx, serverURL, modelName, labelMessages,
		func(choice types.ChatChoice) {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				token := *choice.Delta.Content
				sb.WriteString(token)
				writeSSE("token", fmt.Sprintf("{\"content\":%q}", token))
			}
			if choice.FinishReason != "" {
				content := strings.TrimSpace(sb.String())
				if content == "" {
					content = task.Title
					if content == "" {
						content = "file changes"
					}
				}
				content = buildCommitMessageWithAssistedBy(content, models)
				writeSSE("done", fmt.Sprintf("{\"content\":%q}", content))
			}
		},
		func(progress types.PromptProgressData) {
			total := progress.Total
			if total == 0 {
				total = 1
			}
			pct := (progress.Processed * 100) / total
			writeSSE("prompt_progress", fmt.Sprintf("{\"processed\":%d,\"total\":%d,\"percent\":%d}", progress.Processed, progress.Total, pct))
		},
	)
	if err != nil {
		logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("LLM stream failed: %v", err)
		content := strings.TrimSpace(sb.String())
		if content == "" {
			content = task.Title
			if content == "" {
				content = "file changes"
			}
		}
		content = buildCommitMessageWithAssistedBy(content, models)
		writeSSE("done", fmt.Sprintf("{\"content\":%q}", content))
	}
}

// CommitAndMerge commits the staged worktree changes and optionally merges the task branch.
// Unstaged and untracked work is left alone. The commit queue serializes the git calls per project.
func (m *mergeOrchestrator) CommitAndMerge(projectID, taskID, worktreePath, commitMsg string, autoMerge bool) error {
	release := m.commitQueue.Acquire(projectID)
	defer release()

	_, _ = git.Run(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", commitMsg, "--allow-empty")...)

	if autoMerge {
		task, _ := m.store.GetTaskByID(taskID)
		if err := m.MergeBranch(projectID, taskID, task); err != nil {
			if !MergedDelivered(err) {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("merge failed: %v", err)
			}
			return err
		}
	} else {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge skipped (auto_merge=false)")
	}
	return nil
}

// MergeBranch merges the task's branch into the target branch: a child task merges into its
// parent's branch, a root task into origin. It returns nil on success or when a merge does
// not apply. When the target is checked out in another worktree (a parent created by
// EnsureParentBranch, say) the merge runs inside that worktree, since git refuses to check
// out a branch another worktree already holds.
func (m *mergeOrchestrator) MergeBranch(projectID, taskID string, task *types.Task) error {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	taskBranch := task.Branch
	if taskBranch == "" {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no task branch set, skipping merge")
		return nil
	}

	// A branch that no longer resolves is a skipped merge, not a failure; try a stale sibling first.
	cmd := git.Command(project.Path, "rev-parse", "--verify", "--quiet", taskBranch)
	if err := cmd.Run(); err != nil {
		recovered := m.recoverMissingBranch(projectID, taskID, taskBranch)
		if recovered != "" {
			taskBranch = recovered
		}
		if taskBranch == "" || !m.branchExists(projectID, taskBranch) {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("branch %q is not a valid commit, skipping merge: %v", taskBranch, err)
			return nil
		}
	}

	// Where the merge runs. A child task's target is ALWAYS its parent's branch, never origin.
	var targetBranch string
	var mergeInWorktree string // non-empty: run merge in this worktree path
	if task.ParentTaskID != "" {
		parent, err := m.store.GetTaskByID(task.ParentTaskID)
		if err != nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("parent_task_id=%s not found: %v, aborting merge", task.ParentTaskID, err)
			m.logSystemMessage(taskID, task.CurrentThreadID, "error",
				fmt.Sprintf("Error: parent task %s not found — merge aborted.", task.ParentTaskID))
			return fmt.Errorf("parent task %s not found", task.ParentTaskID)
		}
		if parent.Branch == "" {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("parent %s has no branch, creating it now", parent.ID)
			if err := m.EnsureParentBranch(projectID, parent); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to create parent branch: %v, aborting merge", err)
				m.logSystemMessage(taskID, task.CurrentThreadID, "error",
					fmt.Sprintf("Error: could not create branch for parent task %q — merge aborted: %v", parent.Title, err))
				return fmt.Errorf("create parent branch: %w", err)
			}
			parent, _ = m.store.GetTaskByID(task.ParentTaskID)
		}
		parentWorktreePath, wtErr := m.worktreeMgr.WorktreePath(parent.ID)
		hasWorktree := wtErr == nil && parentWorktreePath != ""
		if hasWorktree {
			if _, statErr := os.Stat(parentWorktreePath); statErr != nil {
				hasWorktree = false
				parentWorktreePath = ""
			}
		}
		if !hasWorktree {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("parent %s has no worktree, creating it now", parent.ID)
			branchName := parent.Branch
			if branchName == "" {
				branchName = m.generateBranchName(parent, parent.ID, projectID)
			}
			if _, _, err := m.worktreeMgr.CreateWorktree(projectID, parent.ID, branchName, ""); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to create parent worktree: %v", err)
			} else {
				parentWorktreePath, _ = m.worktreeMgr.WorktreePath(parent.ID)
				hasWorktree = true
			}
		}
		// The parent's actual checkout wins over its stored branch: the two can differ.
		if hasWorktree && parentWorktreePath != "" {
			mergeInWorktree = parentWorktreePath
			if currentBranch, err := m.worktreeMgr.GitRunInDirOutput(parentWorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				targetBranch = strings.TrimSpace(currentBranch)
			}
		}
		if targetBranch == "" {
			targetBranch = parent.Branch
		}
	} else {
		targetBranch = m.ResolveMergeTarget(projectID, task)
		if targetBranch == "" {
			var err error
			targetBranch, err = m.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("not a git repo, skipping merge: %v", err)
				return nil
			}
		}
	}
	targetBranch = strings.TrimSpace(targetBranch)
	// Create target branch if it doesn't exist (user-specified base_ref).
	if task.ParentTaskID == "" && task.BaseRef != "" && targetBranch == task.BaseRef {
		cmd := git.Command(project.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+targetBranch)
		if cmd.Run() != nil {
			startPoint := project.OriginBranch
			if startPoint == "" {
				startPoint = "HEAD"
			}
			// The start point is a name the repository chose; --end-of-options stops git reading an
			// option-shaped one as a flag of `git branch`.
			createCmd := git.Command("", "-C", project.Path, "branch", targetBranch, "--end-of-options", startPoint)
			if out, err := createCmd.CombinedOutput(); err != nil {
				// A concurrent merge may have created it by now; re-verify before failing.
				recheck := git.Command("", "-C", project.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+targetBranch)
				if recheck.Run() == nil {
					logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("branch %q created by a concurrent merge, proceeding", targetBranch)
				} else {
					logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to create branch %q from %q: %s", targetBranch, startPoint, string(out))
					m.logSystemMessage(taskID, task.CurrentThreadID, "error",
						fmt.Sprintf("Error: could not create branch %q from %q — merge skipped.", targetBranch, startPoint))
					return fmt.Errorf("create branch %q: %w", targetBranch, err)
				}
			} else {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("created branch %q from %q", targetBranch, startPoint)
			}
		}
	}
	if targetBranch == "" || targetBranch == "HEAD" {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no branch (detached HEAD), skipping merge")
		return nil
	}
	logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merging %s into %s (parent_task_id=%s)", taskBranch, targetBranch, task.ParentTaskID)

	// Sync target branch into task branch first (catches conflicts early).
	if syncErr := m.syncTargetIntoTask(projectID, taskID, targetBranch, taskBranch, task); syncErr != nil {
		return fmt.Errorf("sync target into task: %w", syncErr)
	}
	task, _ = m.store.GetTaskByID(taskID)
	if task.HasMergeConflict {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("sync has merge conflict, skipping final merge")
		return nil
	}

	if mergeInWorktree != "" {
		err := m.mergeInWorktree(projectID, mergeInWorktree, targetBranch, taskBranch, taskID, task.ParentTaskID)
		if err == nil && task.ParentTaskID != "" {
			m.broadcastDiffStats(projectID, task.ParentTaskID)
		}
		return err
	}
	// Root tasks only reach mergeInProject (child tasks always merge into the
	// parent's worktree above). The worktree routing for the target branch
	// happens inside it, under the project merge lock.
	err = m.mergeInProject(projectID, targetBranch, taskBranch, taskID)
	if err == nil && task.ParentTaskID != "" {
		m.broadcastDiffStats(projectID, task.ParentTaskID)
	}
	return err
}

// mergeInProject performs a merge in the project's main directory.
func (m *mergeOrchestrator) mergeInProject(projectID, targetBranch, taskBranch, taskID string) error {
	project, _ := m.store.GetProject(projectID)
	// Serialize against other merges on this project's main checkout: it has a
	// single HEAD/index, so interleaved merges clobber each other's checkout,
	// stashes, and restore. Held for the whole sequence below (capture →
	// merge → restore → stash pop). Lock order: before the project stash mutex.
	defer m.worktreeMgr.LockProjectMerge(projectID)()
	// Prune stale worktree registrations so a deleted worktree doesn't block checkout.
	m.worktreeMgr.GitRunInProject(projectID, "worktree", "prune")
	// If the target branch is checked out in a worktree, merge there instead.
	// This check must run under the merge lock: an in-flight main-checkout
	// merge temporarily checks the target out in the main checkout, which an
	// unlocked scan would mis-route to (skipping both the lock and the
	// checkout restore).
	if wtPath := m.findWorktreeForBranch(projectID, targetBranch); wtPath != "" {
		return m.mergeInWorktree(projectID, wtPath, targetBranch, taskBranch, taskID, "")
	}
	// The merge moves the project's main checkout; it goes back to wherever the user had it.
	return m.mergeInto(projectID, project.Path, targetBranch, taskBranch, taskID, currentCheckoutRef(project.Path))
}

// mergeInWorktree merges inside another task's worktree, serialized against that task's tools.
func (m *mergeOrchestrator) mergeInWorktree(projectID, worktreePath, targetBranch, taskBranch, taskID, ownerTaskID string) error {
	if ownerTaskID != "" {
		defer m.lockWorktree(ownerTaskID)()
	}
	// A worktree stays on its own branch: nothing to restore.
	return m.mergeInto(projectID, worktreePath, targetBranch, taskBranch, taskID, "")
}

// mergeInto merges taskBranch into targetBranch in dir. restoreRef, when it differs from
// targetBranch, is the checkout dir is put back on afterwards.
//
// `landed` separates the bracket's two failures: with both joined, the restore conflict is still reachable
// inside a failed merge, and only the merge outcome decides whether the task finished.
func (m *mergeOrchestrator) mergeInto(projectID, dir, targetBranch, taskBranch, taskID, restoreRef string) error {
	landed := false
	bracketErr := m.worktreeMgr.WithStash(projectID, dir, git.StashMessageForAutoMerge(taskID), func() error {
		mergeErr := m.mergeOnTargetBranch(projectID, dir, targetBranch, taskBranch, taskID)
		landed = mergeErr == nil
		// Back off the caller's checkout before the bracket restores the stashed work, so it lands
		// on the branch the directory belongs to rather than on the merge target.
		if restoreRef != "" && restoreRef != targetBranch {
			if _, err := m.worktreeMgr.GitRunInDirOutput(dir, "checkout", "-f", restoreRef); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to restore checkout to %s: %v", restoreRef, err)
			}
		}
		return mergeErr
	})
	var conflict *git.RestoreConflictError
	if landed && errors.As(bracketErr, &conflict) {
		m.logConflictedRestore(taskID, targetBranch, conflict)
		return fmt.Errorf("%w: %w", errMergedWithConflictedRestore, bracketErr)
	}
	return bracketErr
}

// errMergedWithConflictedRestore is a merge that delivered the work to the target branch, followed by a
// restore of the checkout's own parked work that conflicted. mergeInto adds it only when the merge landed,
// which is what MergedDelivered is testing for.
var errMergedWithConflictedRestore = errors.New("merged, but the parked work came back conflicted")

// MergedDelivered is the Done test for a merge error: the branch reached its target. A conflicted restore
// of the project's own parked work leaves the project's working copy holding the conflict and does not
// undo that.
//
// The merge-conflict resolver must never be pointed at such a task: it answers a conflict by editing the
// file and running `git add`, which is git's signal that the conflict is resolved, and what is conflicted
// here is the person's own uncommitted text.
func MergedDelivered(mergeErr error) bool {
	return mergeErr == nil || errors.Is(mergeErr, errMergedWithConflictedRestore)
}

// logConflictedRestore is the only announcement that a file was left with conflict markers by a merge that
// succeeded, so it names the file and where the parked version still is.
func (m *mergeOrchestrator) logConflictedRestore(taskID, targetBranch string, conflict *git.RestoreConflictError) {
	task, _ := m.store.GetTaskByID(taskID)
	if task == nil {
		return
	}
	logging.For(logging.Merge).With(logging.TaskID, taskID).With(logging.ProjectID, task.ProjectID).Warnf(
		"merged into %s but the project's own work could not be restored: %v", targetBranch, conflict)
	m.logSystemMessage(taskID, task.CurrentThreadID, "warning", fmt.Sprintf(
		"Merged into %s. Your own uncommitted changes to %s could not be restored on top of the merge: "+
			"the file is left with conflict markers in the project checkout, and your version is still in the "+
			"project's stash (git stash list → %s). Resolve it there — this task is done.",
		targetBranch, strings.Join(conflict.Paths, ", "), git.StashMessageForAutoMerge(taskID)))
}

// mergeOnTargetBranch checks dir out on targetBranch and merges taskBranch into it, recording the
// outcome. dir's own work is parked by the caller's stash bracket — nothing here may touch the
// working tree before the checkout.
func (m *mergeOrchestrator) mergeOnTargetBranch(projectID, dir, targetBranch, taskBranch, taskID string) error {
	if err := m.worktreeMgr.GitRunInDir(dir, "checkout", "-f", targetBranch); err != nil {
		return fmt.Errorf("checkout target branch %s: %w", targetBranch, err)
	}

	mergeOut, mergeErr := m.worktreeMgr.GitRunInDirOutput(dir, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		task, _ := m.store.GetTaskByID(taskID)
		if resolveErr := m.tryResolveMergeConflicts(dir, taskID, targetBranch, taskBranch, task); resolveErr != nil {
			if errors.Is(resolveErr, ErrNotAMergeConflict) {
				// Some other git failure (hook, lock): show git's own reason.
				mergeDetail := strings.TrimSpace(mergeOut)
				if mergeDetail == "" {
					mergeDetail = mergeErr.Error()
				}
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("merge failed (not a conflict): %s", mergeDetail)
				m.persistAndBroadcastMergeMessage(taskID, task.CurrentThreadID,
					fmt.Sprintf("Merge failed: %s", mergeDetail), "Merge failed", true)
				return fmt.Errorf("merge failed (not a conflict): %s", mergeDetail)
			}
			// Flag it and abandon this merge: the model resolves the conflicts in a later turn.
			_ = m.store.SetTaskMergeConflict(taskID, projectID, true)
			_ = m.worktreeMgr.GitRunInDir(dir, "merge", "--abort")
			m.persistAndBroadcastMergeMessage(taskID, task.CurrentThreadID,
				fmt.Sprintf("Merge failed: %v", resolveErr), "Merge failed", true)
			return fmt.Errorf("merge conflict detected (will resolve via LLM): %w", resolveErr)
		}
	} else {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merged %s into %s successfully", taskBranch, targetBranch)
		task, _ := m.store.GetTaskByID(taskID)
		m.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
	}
	return nil
}

// findWorktreeForBranch returns the path of the worktree holding that branch, or "". git refuses to
// check out a branch a worktree already holds, so a merge into the main checkout needs to know.
func (m *mergeOrchestrator) findWorktreeForBranch(projectID, branch string) string {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return ""
	}
	cmd := git.Command(project.Path, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	worktrees := strings.Split(string(out), "\n")
	var currentPath, currentBranch string
	for _, line := range worktrees {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
			currentBranch = ""
		} else if strings.HasPrefix(line, "branch ") {
			currentBranch = strings.TrimPrefix(line, "branch refs/heads/")
		}
		if currentBranch == branch && currentPath != "" {
			if _, err := os.Stat(currentPath); err == nil {
				return currentPath
			}
		}
	}
	return ""
}

// currentCheckoutRef returns dir's HEAD as a branch name, or a SHA when detached. "" means
// unknown, and the merge then leaves the checkout where it ends up.
func currentCheckoutRef(dir string) string {
	cmd := git.Command(dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" || ref == "HEAD" {
		cmd = git.Command(dir, "rev-parse", "HEAD")
		out, err = cmd.Output()
		if err != nil {
			return ""
		}
		ref = strings.TrimSpace(string(out))
	}
	return ref
}

// syncTargetIntoTask merges the target branch into the task's branch, so conflicts surface while
// the model still has the task's changes in context. It returns nil on a clean merge, on nothing
// to merge, and on entering merge-conflict state — MergeBranch checks task.HasMergeConflict and
// skips the final merge then. Every other failure, the stash's included, is returned, so the final
// merge never runs on a worktree that could not be moved safely.
func (m *mergeOrchestrator) syncTargetIntoTask(projectID, taskID, targetBranch, taskBranch string, task *types.Task) error {
	// Serialize against the task's own tool execution.
	defer m.lockWorktree(taskID)()

	project, err := m.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project for sync: %w", err)
	}

	// merge-base is the common ancestor: if it equals the target's HEAD, the target has not moved.
	mergeBaseCmd := git.Command(project.Path, "merge-base", taskBranch, targetBranch)
	mergeBaseOut, err := mergeBaseCmd.Output()
	if err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("could not compute merge-base: %v, skipping sync", err)
		return nil
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))

	targetHeadCmd := git.Command(project.Path, "rev-parse", targetBranch)
	targetHeadOut, err := targetHeadCmd.Output()
	if err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("could not resolve target HEAD: %v, skipping sync", err)
		return nil
	}
	targetHead := strings.TrimSpace(string(targetHeadOut))

	if mergeBase == targetHead {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("target %s has no new commits, skipping sync", targetBranch)
		return nil
	}

	logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("target %s has new commits (merge-base=%s, target=%s), syncing", targetBranch, mergeBase[:7], targetHead[:7])

	worktreePath, err := m.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		return fmt.Errorf("get worktree path for sync: %w", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("worktree does not exist, skipping sync")
		return nil
	}

	syncErr := m.worktreeMgr.WithStash(projectID, worktreePath, git.StashMessageForSyncMerge(taskID), func() error {
		if err := m.worktreeMgr.GitRunInDir(worktreePath, "checkout", "-f", taskBranch); err != nil {
			return fmt.Errorf("checkout task branch %s for sync: %w", taskBranch, err)
		}

		if _, mergeErr := m.worktreeMgr.GitRunInDirOutput(worktreePath, "merge", targetBranch, "--no-edit"); mergeErr == nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("synced %s into %s cleanly", targetBranch, taskBranch)
			m.logSystemMessage(taskID, task.CurrentThreadID, "info",
				fmt.Sprintf("Synced %s into task branch (no conflicts).", targetBranch))
			return nil
		}

		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("conflict syncing %s into %s, setting merge conflict flag", targetBranch, taskBranch)
		_ = m.store.SetTaskMergeConflict(taskID, projectID, true)
		task.HasMergeConflict = true
		m.logSystemMessage(taskID, task.CurrentThreadID, "warning",
			fmt.Sprintf("Conflicts detected when syncing %s into task branch. Resolve the conflicted files using your tools — the backend will commit and continue the merge automatically.", targetBranch))
		m.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		// Leave the merge open here, unlike the other path: the model resolves it, and the stash
		// is popped after merge --continue.
		return git.ErrKeepStash
	})
	if errors.Is(syncErr, git.ErrKeepStash) {
		return nil
	}
	return syncErr
}

// conflictResolution is the outcome of commitConflictResolution: everything a caller needs to decide
// what the task becomes once the conflict is gone.
type conflictResolution struct {
	// task is the task as the store holds it now. Never nil.
	task *types.Task
	// committed records that the resolution became a commit. Nothing merges without it.
	committed bool
	// mergeErr is the error from the merge into the target branch, nil when it ran cleanly or
	// was not wanted (see mergeWanted).
	mergeErr error
	// commitDone says a "Commit and move to done" click produced this conflict.
	commitDone bool
}

// mergeWanted reports whether anything asked for this work to land in the target branch: the
// task's auto_merge setting, or the user's click.
func (r conflictResolution) mergeWanted() bool {
	return r.commitDone || (r.task != nil && r.task.AutoMerge)
}

// finishesTask reports whether the resolution delivered what mergeWanted asked for: committed, the work
// in the target branch, nothing re-conflicted. Otherwise the task goes back to the user.
func (r conflictResolution) finishesTask() bool {
	return r.committed && r.mergeWanted() && MergedDelivered(r.mergeErr) && r.task != nil && !r.task.HasMergeConflict
}

// tryResolveMergeConflicts classifies a failed merge in dir: with no conflicted files git finished
// the merge itself and --continue completes it; anything else is reported by naming the files. A
// real conflict is aborted, never resolved here.
func (m *mergeOrchestrator) tryResolveMergeConflicts(dir, taskID, targetBranch, taskBranch string, task *types.Task) error {
	if !isMergeInProgress(dir) {
		return fmt.Errorf("merge failed but not in merge state (no MERGE_HEAD): %w", ErrNotAMergeConflict)
	}

	conflictedFiles := GetConflictedFiles(dir)

	// An empty conflict list means git resolved it itself; let it finish.
	if len(conflictedFiles) == 0 {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no conflicted files found, trying merge --continue")
		_, continueErr := git.Run(dir, append(git.GitAuthorFlags(), "merge", "--continue")...)
		if continueErr == nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge auto-resolved via --continue")
			m.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
			return nil
		}
		statusOutput := getMergeStatusOutput(dir)
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("merge --continue failed: %v. Status: %s", continueErr, statusOutput)
		_, _ = git.Run(dir, "merge", "--abort")
		return fmt.Errorf("merge conflict but no conflicted files found (status: %s)", statusOutput)
	}

	logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("conflict in %d file(s): %s", len(conflictedFiles), strings.Join(conflictedFiles, ", "))

	m.logSystemMessage(taskID, task.CurrentThreadID, "error",
		fmt.Sprintf("Merge conflict detected in: %s", strings.Join(conflictedFiles, ", ")))

	_, _ = git.Run(dir, "merge", "--abort")
	return fmt.Errorf("merge conflict in %d file(s): %s", len(conflictedFiles), strings.Join(conflictedFiles, ", "))
}

func (m *mergeOrchestrator) logMergeComplete(taskID, threadID, taskBranch, targetBranch string) {
	content := fmt.Sprintf("Branch %s merged into %s", taskBranch, targetBranch)
	m.persistAndBroadcastMergeMessage(taskID, threadID, content, "Merged", false)
}

// persistAndBroadcastMergeMessage creates a merge_resolution message and broadcasts it. A thread of
// its own is what makes the announcement readable later, so no thread means no message at all.
// isError marks the frame as a failure; the key is absent otherwise, which is what the client reads
// as false.
func (m *mergeOrchestrator) persistAndBroadcastMergeMessage(taskID, threadID, content, label string, isError bool) {
	if threadID == "" {
		return
	}
	msgID := uuid.New().String()
	if err := m.store.CreateTaskMessage(&types.TaskMessage{
		ID:        msgID,
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "merge_resolution",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to log message: %v", err)
		return
	}
	// Broadcast to task stream clients for real-time display
	task, _ := m.store.GetTaskByID(taskID)
	if task != nil && m.appStreamMgr != nil {
		data := map[string]any{
			"id":         msgID,
			"content":    content,
			"label":      label,
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}
		if isError {
			data["is_error"] = true
		}
		m.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeMergeResolution, Data: data})
	}
}

// stageResolvedConflicts stages every file whose conflict markers are gone. Nothing recorded as
// resolved is still a success: the commit below is what records the resolution. The names come
// from git's own listing, so they follow "--" and cannot be read as options of git add.
func (m *mergeOrchestrator) stageResolvedConflicts(taskID, worktreePath string) bool {
	conflictedFiles := GetResolvedConflictedFiles(worktreePath)
	if len(conflictedFiles) == 0 {
		return true
	}
	addArgs := append([]string{"add", "--"}, conflictedFiles...)
	if _, err := git.Run(worktreePath, addArgs...); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git add failed: %v", err)
		return false
	}
	return true
}

// commitMergeConflictResolution records a resolution as a commit. Both resolution paths commit
// here so the message and author stay identical.
func (m *mergeOrchestrator) commitMergeConflictResolution(taskID, worktreePath string) bool {
	if _, err := git.Run(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git commit failed: %v", err)
		return false
	}
	return true
}

// commitConflictResolution runs the git work every conflict resolution shares: stage, commit,
// drop the stale stash, clear the flag, attempt the final merge. Callers handle what differs
// afterwards — inject a prompt, mark done, or fail.
func (m *mergeOrchestrator) commitConflictResolution(projectID, taskID, worktreePath string, task *types.Task) conflictResolution {
	// No worktree mutex here: MergeBranch→syncTargetIntoTask takes it and would deadlock.
	// Safe because the LLM is done and the loop is single-threaded.
	commitDone := m.CommitAndDonePending(taskID)
	commitOK := true

	if isMergeInProgress(worktreePath) {
		// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
		commitOK = m.stageResolvedConflicts(taskID, worktreePath)
	} else {
		// Final-merge conflict: worktree has no MERGE_HEAD.
		checkCmd := git.Command(worktreePath, "diff", "--quiet")
		if checkCmd.Run() == nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no changes to commit for conflict resolution")
			commitOK = false
		}
		if commitOK {
			// `add -u` stages only tracked changes — never sweeps untracked files.
			if _, err := git.Run(worktreePath, "add", "-u"); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git add -u failed: %v", err)
				commitOK = false
			}
		}
	}

	if commitOK {
		commitOK = m.commitMergeConflictResolution(taskID, worktreePath)
	}

	if commitOK {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("conflict resolution committed")
		// The pre-sync edits this stash holds are committed by now. Popping would re-apply them as
		// unstaged changes; a bare drop would take stash@{0} — see DropStashForTask.
		//
		// Released explicitly, not deferred: MergeBranch below takes the merge lock, which must
		// come before the stash lock, never after.
		unlockStash := m.worktreeMgr.LockProjectStash(projectID)
		if _, err := m.worktreeMgr.DropStashForTask(worktreePath, git.StashMessageForSyncMerge(taskID)); err != nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("discarding the sync-merge stash: %v", err)
		}
		unlockStash()
	}

	_ = m.store.SetTaskMergeConflict(taskID, projectID, false)

	var mergeErr error
	// A click merges whatever auto_merge says; without one, auto_merge alone decides.
	if commitOK && (task.AutoMerge || commitDone) {
		mergeErr = m.MergeBranch(projectID, taskID, task)
		if mergeErr != nil && !MergedDelivered(mergeErr) {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("final merge failed: %v", mergeErr)
		}
	} else if commitOK {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge skipped (auto_merge=false, no commit-and-done click)")
	}

	updatedTask, err := m.store.GetTaskByID(taskID)
	if err != nil || updatedTask == nil {
		// Callers overwrite their own task with this, so a store hiccup must not hand them a nil.
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("refresh after conflict resolution failed: %v", err)
		updatedTask = task
	}

	res := conflictResolution{task: updatedTask, committed: commitOK, mergeErr: mergeErr, commitDone: commitDone}
	if !updatedTask.HasMergeConflict {
		// The conflict is over, so the click is spent, merge failure included; a still conflicted
		// task keeps it for the turn that finishes it.
		res.commitDone = m.takeCommitAndDone(taskID, MergedDelivered(mergeErr))
	}
	return res
}

// ArmCommitAndDone records that a "Commit and move to done" click produced this task's merge
// conflict. Idempotent: clicking again states the same intent.
func (m *mergeOrchestrator) ArmCommitAndDone(taskID string) {
	m.commitDone.mu.Lock()
	defer m.commitDone.mu.Unlock()
	m.commitDone.tasks[taskID] = struct{}{}
}

// CommitAndDonePending reports whether a click is recorded, without consuming it.
func (m *mergeOrchestrator) CommitAndDonePending(taskID string) bool {
	m.commitDone.mu.Lock()
	defer m.commitDone.mu.Unlock()
	_, ok := m.commitDone.tasks[taskID]
	return ok
}

// takeCommitAndDone consumes the click and reports whether the task is done by it: a click was
// recorded AND mergeOK. A failed merge spends the click too. Call it only once the conflict is
// over — a still conflicted resolution needs the click for the turn that finishes.
func (m *mergeOrchestrator) takeCommitAndDone(taskID string, mergeOK bool) bool {
	m.commitDone.mu.Lock()
	defer m.commitDone.mu.Unlock()
	if _, ok := m.commitDone.tasks[taskID]; !ok {
		return false
	}
	delete(m.commitDone.tasks, taskID)
	return mergeOK
}

// clearCommitAndDone drops the click without acting on it: the user has taken over, so what they said
// is now the intent.
func (m *mergeOrchestrator) clearCommitAndDone(taskID string) {
	m.commitDone.mu.Lock()
	defer m.commitDone.mu.Unlock()
	delete(m.commitDone.tasks, taskID)
}
