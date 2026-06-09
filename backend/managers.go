package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

var taskWords = []string{
	// Nature
	"ocean", "river", "mountain", "forest", "desert", "island", "valley", "meadow",
	"garden", "stream", "canyon", "glacier", "volcano", "waterfall", "sunrise", "sunset",
	"thunder", "lightning", "rainbow", "aurora", "breeze", "storm", "frost", "snow",
	// Animals
	"eagle", "wolf", "bear", "fox", "hawk", "owl", "deer", "lion",
	"tiger", "shark", "dolphin", "whale", "panda", "falcon", "raven", "heron",
	// Colors
	"crimson", "azure", "emerald", "amber", "violet", "coral", "sapphire", "ruby",
	"gold", "silver", "bronze", "copper", "ivory", "pearl", "onyx", "jade",
	// Space
	"nova", "comet", "meteor", "galaxy", "nebula", "planet", "star", "cosmos",
	"orbit", "pulse", "zenith", "eclipse", "lunar", "solar", "stellar",
	// Elements
	"flame", "stone", "iron", "steel", "crystal", "diamond", "obsidian", "marble",
	"granite", "basalt", "quartz", "flint", "coal", "ash", "ember", "spark",
	// Abstract
	"dream", "vision", "spirit", "soul", "mind", "heart", "voice", "echo",
	"shadow", "light", "truth", "wisdom", "power", "grace", "honor", "valor",
}

func generateUUID() string {
	return uuid.New().String()
}

func generateTaskID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	w3 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2 + "-" + w3
}

// -- TaskManager --

// TaskManager handles task CRUD operations.
type TaskManager struct {
	store          *Store
	boardStreamMgr *BoardStreamManager
	configStore    *ConfigStore
	terminalMgr    *TerminalManager
	executor       *TaskExecutor
	worktreeMgr    *WorktreeManager
}

// NewTaskManager creates a new TaskManager.
func NewTaskManager(store *Store, boardStreamMgr *BoardStreamManager, configStore *ConfigStore, terminalMgr *TerminalManager, executor *TaskExecutor, worktreeMgr *WorktreeManager) *TaskManager {
	return &TaskManager{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, terminalMgr: terminalMgr, executor: executor, worktreeMgr: worktreeMgr}
}

// HandleList lists or creates tasks for a project.
func (tm *TaskManager) HandleList(w http.ResponseWriter, r *http.Request, projectID string) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := tm.store.GetTasksByProject(projectID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, tasks)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var task Task
		if err := json.Unmarshal(body, &task); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		task.ID = generateTaskID()
		task.ProjectID = projectID
		if task.Status == "" {
			task.Status = "backlog"
		}
		now := time.Now().UTC()
		task.CreatedAt = now
		task.UpdatedAt = now
		// Auto-generate title from prompt if not provided
		if task.Title == "" && task.Prompt != "" {
			task.Title = autoGenerateTitle(task.Prompt)
		}
		// Apply config defaults for new tasks
		if tm.configStore != nil {
			if cfg, err := tm.configStore.GetConfig(); err == nil {
				if cfg.DefaultAutoCommit != nil && task.AutoCommit == false {
					task.AutoCommit = *cfg.DefaultAutoCommit
				}
				if cfg.DefaultPlanMode != nil && task.PlanMode == false {
					task.PlanMode = *cfg.DefaultPlanMode
				}
				if cfg.DefaultMaxFailedTurns != nil && task.MaxConsecutiveFailedTurns == 0 {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
				}
			}
		}
		if err := tm.store.CreateTask(&task); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		tm.boardStreamMgr.Broadcast(projectID)
		jsonResponse(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleByID handles GET/PUT/DELETE for a single task.
func (tm *TaskManager) HandleByID(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		task, err := tm.store.GetTask(projectID, taskID)
		if err != nil {
			jsonError(w, http.StatusNotFound, "task not found")
			return
		}
		jsonResponse(w, http.StatusOK, task)
	case http.MethodPut:
		task, err := tm.store.GetTask(projectID, taskID)
		if err != nil {
			jsonError(w, http.StatusNotFound, "task not found")
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, task)
		task.ID = taskID
		task.ProjectID = projectID
		task.UpdatedAt = time.Now()
		if err := tm.store.UpdateTask(task); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		tm.boardStreamMgr.Broadcast(projectID)
		jsonResponse(w, http.StatusOK, task)
	case http.MethodDelete:
		// Verify task exists before cleanup
		if _, err := tm.store.GetTask(projectID, taskID); err != nil {
			jsonError(w, http.StatusNotFound, "task not found")
			return
		}
		// 1. Stop running executor (best-effort)
		if tm.executor != nil {
			_ = tm.executor.StopTask(taskID)
		}
		// 2. Remove stream broadcaster (best-effort)
		if tm.executor != nil {
			tm.executor.RemoveStream(taskID)
		}
		// 3. Remove worktree directory (best-effort, log errors)
		if tm.worktreeMgr != nil {
			if err := tm.worktreeMgr.RemoveWorktreeDir(projectID, taskID); err != nil {
				log.Printf("task delete: worktree cleanup failed for %s: %v", taskID, err)
			}
		}
		// 4. Remove terminal session
		tm.terminalMgr.removeSession(taskID)
		// 5. Delete from database (CASCADE removes messages, dependencies, review comments)
		if err := tm.store.DeleteTask(projectID, taskID); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 6. Broadcast board update
		tm.boardStreamMgr.Broadcast(projectID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleClearDone deletes all tasks with status 'done' for a project.
func (tm *TaskManager) HandleClearDone(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all done tasks
	tasks, err := tm.store.GetTasksByProject(projectID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}

	var deleted int
	for _, task := range tasks {
		if task.Status != "done" {
			continue
		}
		taskID := task.ID

		// 1. Stop running executor (best-effort)
		if tm.executor != nil {
			_ = tm.executor.StopTask(taskID)
		}
		// 2. Remove stream broadcaster (best-effort)
		if tm.executor != nil {
			tm.executor.RemoveStream(taskID)
		}
		// 3. Remove worktree directory (best-effort, log errors)
		if tm.worktreeMgr != nil {
			if err := tm.worktreeMgr.RemoveWorktreeDir(projectID, taskID); err != nil {
				log.Printf("clear done: worktree cleanup failed for %s: %v", taskID, err)
			}
		}
		// 4. Remove terminal session
		tm.terminalMgr.removeSession(taskID)
		// 5. Delete from database (CASCADE removes messages, dependencies, review comments)
		if err := tm.store.DeleteTask(projectID, taskID); err != nil {
			log.Printf("clear done: db delete failed for %s: %v", taskID, err)
		}
		deleted++
	}

	// 6. Broadcast board update
	tm.boardStreamMgr.Broadcast(projectID)
	jsonResponse(w, http.StatusOK, map[string]interface{}{"deleted": deleted})
}

// HandleMove moves a task to a different column.
func (tm *TaskManager) HandleMove(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req MoveTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	task, err := tm.store.GetTask(projectID, taskID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "task not found")
		return
	}
	task.Status = req.Status
	task.UpdatedAt = time.Now()

	if req.Status == "in_progress" && task.StatusState != "running" {
		// Auto-start the task when moved to In Progress
		if err := tm.executor.StartTask(projectID, taskID); err != nil {
			log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
			task.StatusState = "failed"
			task.Output = err.Error()
		}
	}
	if req.Status == "done" {
		task.StatusState = "completed"
	}
	if err := tm.store.UpdateTask(task); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tm.boardStreamMgr.Broadcast(projectID)
	jsonResponse(w, http.StatusOK, task)
}

// -- DependencyManager --

// DependencyManager handles task dependency operations.
type DependencyManager struct {
	store *Store
}

// NewDependencyManager creates a new DependencyManager.
func NewDependencyManager(store *Store) *DependencyManager {
	return &DependencyManager{store: store}
}

// HandleDependencies lists or creates dependencies for a task.
func (dm *DependencyManager) HandleDependencies(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		deps, err := dm.store.GetDependents(taskID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, deps)
	case http.MethodPost:
		var req struct {
			ChildTaskID string `json:"child_task_id"`
			AutoStart   bool   `json:"auto_start"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		dep := &TaskDependency{
			ID:           uuid.New().String(),
			ParentTaskID: taskID,
			ChildTaskID:  req.ChildTaskID,
			AutoStart:    req.AutoStart,
			CreatedAt:    time.Now().UTC(),
		}
		if err := dm.store.CreateDependency(dep); err != nil {
			if strings.Contains(err.Error(), "circular") {
				jsonError(w, http.StatusConflict, err.Error())
			} else {
				jsonError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		jsonResponse(w, http.StatusCreated, dep)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleDependency handles DELETE for a specific dependency.
func (dm *DependencyManager) HandleDependency(w http.ResponseWriter, r *http.Request, projectID, taskID, childTaskID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := dm.store.DeleteDependency(taskID, childTaskID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDependents lists tasks that depend on this task.
func (dm *DependencyManager) HandleDependents(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deps, err := dm.store.GetDependencies(taskID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, deps)
}

// -- ReviewManager --

// ReviewManager handles review comment operations.
type ReviewManager struct {
	store *Store
}

// NewReviewManager creates a new ReviewManager.
func NewReviewManager(store *Store) *ReviewManager {
	return &ReviewManager{store: store}
}

// HandleComments lists or creates review comments for a task.
func (rm *ReviewManager) HandleComments(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		comments, err := rm.store.GetReviewComments(taskID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, comments)
	case http.MethodPost:
		var req struct {
			FilePath string `json:"file_path"`
			LineNum  int    `json:"line_num"`
			Comment  string `json:"comment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		comment := &ReviewComment{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			FilePath:  req.FilePath,
			LineNum:   req.LineNum,
			Comment:   req.Comment,
			Resolved:  false,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := rm.store.CreateReviewComment(comment); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusCreated, comment)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleComment handles GET/PUT/DELETE for a single review comment.
func (rm *ReviewManager) HandleComment(w http.ResponseWriter, r *http.Request, projectID, taskID, commentID string) {
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Comment  *string `json:"comment"`
			Resolved *bool   `json:"resolved"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		comment, err := rm.store.UpdateReviewComment(commentID, req.Comment, req.Resolved)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, comment)
	case http.MethodDelete:
		if err := rm.store.DeleteReviewComment(commentID); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// -- GitManager --

// GitManager handles git operations.
type GitManager struct {
	store *Store
}

// NewGitManager creates a new GitManager.
func NewGitManager(store *Store) *GitManager {
	return &GitManager{store: store}
}

// HandleLog returns commit log for a project.
func (gm *GitManager) HandleLog(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "HEAD"
	}
	c := exec.Command("git", "-C", project.Path, "log", "--oneline", "--decorate", branch)
	out, err := c.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []GitCommit
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		commit := GitCommit{Short: parts[0], Subject: parts[1]}
		commits = append(commits, commit)
	}
	json.NewEncoder(w).Encode(commits)
}

// HandleBranches lists branches for a project.
func (gm *GitManager) HandleBranches(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "branch", "-a", "--format=%(refname:short)")
	out, err := c.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git branches failed: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var branches []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	jsonResponse(w, http.StatusOK, branches)
}

// HandleDiff returns diff between refs.
func (gm *GitManager) HandleDiff(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "HEAD"
	}
	c := exec.Command("git", "-C", project.Path, "diff", ref)
	out, err := c.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// HandleStatus returns working tree status.
func (gm *GitManager) HandleStatus(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "status", "--porcelain")
	out, err := c.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	jsonResponse(w, http.StatusOK, map[string]interface{}{"clean": clean, "status": string(out)})
}

// HandleCheckout checks out a branch.
func (gm *GitManager) HandleCheckout(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	c := exec.Command("git", "-C", project.Path, "checkout", req.Branch)
	if out, err := c.CombinedOutput(); err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v: %s", err, out))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"branch": req.Branch})
}

// HandlePull pulls from remote.
func (gm *GitManager) HandlePull(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "pull")
	if out, err := c.CombinedOutput(); err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git pull failed: %v: %s", err, out))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "pulled"})
}

// HandlePush pushes to remote.
func (gm *GitManager) HandlePush(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := gm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "push")
	if out, err := c.CombinedOutput(); err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %v: %s", err, out))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "pushed"})
}

// HandleTaskDiff returns diff for a task's worktree.
func (gm *GitManager) HandleTaskDiff(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	task, err := gm.store.GetTask(projectID, taskID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "task not found")
		return
	}
	worktreesDir := getEnv("WORKTREES_DIR", "./.worktrees")
	if !filepath.IsAbs(worktreesDir) {
		if abs, err := filepath.Abs(worktreesDir); err == nil {
			worktreesDir = abs
		}
	}
	worktreePath := fmt.Sprintf("%s/%s", worktreesDir, task.ID)
	c := exec.Command("git", "-C", worktreePath, "diff")
	out, err := c.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// -- HomeAgentManager --

// HomeAgentManager handles home agent operations.
type HomeAgentManager struct {
	store       *Store
	configStore *ConfigStore
	streamMgr   *StreamManager
}

// NewHomeAgentManager creates a new HomeAgentManager.
func NewHomeAgentManager(store *Store, configStore *ConfigStore) *HomeAgentManager {
	return &HomeAgentManager{
		store:       store,
		configStore: configStore,
		streamMgr:   NewStreamManager(store),
	}
}

// HandleMessage sends a message to the home agent.
func (ham *HomeAgentManager) HandleMessage(w http.ResponseWriter, r *http.Request, projectID string) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	msg := &HomeAgentMessage{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		Role:      "user",
		Content:   req.Content,
		CreatedAt: time.Now().UTC(),
	}
	if err := ham.store.CreateHomeAgentMessage(msg); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, msg)
}

// HandleMessages gets home agent conversation.
func (ham *HomeAgentManager) HandleMessages(w http.ResponseWriter, r *http.Request, projectID string) {
	msgs, err := ham.store.GetHomeAgentMessages(projectID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, msgs)
}

// HandleStream handles WebSocket stream for home agent.
func (ham *HomeAgentManager) HandleStream(w http.ResponseWriter, r *http.Request, projectID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	// Send welcome message
	welcome := HomeAgentMessage{Content: "Home agent connected. How can I help?", Role: "assistant"}
	data, _ := json.Marshal(map[string]interface{}{"type": "assistant_token", "data": welcome})
	conn.Write(r.Context(), websocket.MessageText, data)

	// Echo loop (TODO: integrate with AI)
	for {
		_, msg, err := conn.Read(r.Context())
		if err != nil {
			break
		}
		var input struct {
			Type string `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg, &input); err != nil {
			continue
		}
		if input.Type == "user_message" {
			var content struct {
				Content string `json:"content"`
			}
			json.Unmarshal(input.Data, &content)
			// Persist user message
			userMsg := &HomeAgentMessage{ID: uuid.New().String(), ProjectID: projectID, Role: "user", Content: content.Content, CreatedAt: time.Now().UTC()}
			ham.store.CreateHomeAgentMessage(userMsg)
			// Echo back (placeholder for AI response)
			response := HomeAgentMessage{Content: "I received: " + content.Content, Role: "assistant"}
			respData, _ := json.Marshal(map[string]interface{}{"type": "assistant_token", "data": response})
			conn.Write(r.Context(), websocket.MessageText, respData)
		}
	}
}

// autoGenerateTitle creates a short title from the first line of a prompt.
func autoGenerateTitle(prompt string) string {
	// Take the first line, up to 80 chars
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}

