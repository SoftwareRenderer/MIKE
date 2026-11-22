package api

// Creating an extension is one click that makes a repository, a commit, a project and a started task. It
// is the only place MIKE writes into the extensions tree for a person, so the sequence either lands or
// leaves nothing: a half-written repository, or one no project owns, is a state nobody can clean up.

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"mike/backend/config"
	"mike/backend/extensions"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"

	"github.com/google/uuid"
)

// ExtensionScaffoldDeps is what the create path needs beyond the handlers it reuses. The authoring
// bundle is injected because a package under backend/ must not reach for the source tree.
type ExtensionScaffoldDeps struct {
	Store       *store.Store
	Projects    *ProjectHandler
	Tasks       *TaskHandler
	Extensions  *extensions.ExtensionHandler
	ConfigStore *config.ConfigStore
}

// ExtensionScaffoldHandler creates a custom extension beside its own repository.
type ExtensionScaffoldHandler struct {
	store       *store.Store
	projects    *ProjectHandler
	tasks       *TaskHandler
	ext         *extensions.ExtensionHandler
	configStore *config.ConfigStore
}

// NewExtensionScaffoldHandler wires the create path over the handlers it borrows: the project's own
// creation validates the repository, and the task's own start decides whether the agent runs now.
func NewExtensionScaffoldHandler(deps ExtensionScaffoldDeps) *ExtensionScaffoldHandler {
	return &ExtensionScaffoldHandler{
		store:       deps.Store,
		projects:    deps.Projects,
		tasks:       deps.Tasks,
		ext:         deps.Extensions,
		configStore: deps.ConfigStore,
	}
}

// scaffoldRequest is decoded into pointer fields, so a body that sends neither field is refused
// differently from one that sends them empty.
type scaffoldRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// devTaskPreamble is the only text the create path adds to the prompt; the person's description follows
// it verbatim.
const devTaskPreamble = "You are writing a MIKE extension in this repository.\n\n" +
	"Write manifest.json at the repository root beside the files it names, and copy from the skill's " +
	"examples rather than inventing a shape.\n\n" +
	"When you finish, say which type you chose, why, and what the person should look at after they " +
	"press Try Extension.\n\n" +
	"What the person asked for:\n"

// HandleScaffold answers POST /api/extensions/scaffold. The person supplies a name and one
// description; the type, the manifest and every file come from the agent the task starts.
func (h *ExtensionScaffoldHandler) HandleScaffold(w http.ResponseWriter, r *http.Request) {
	var req scaffoldRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(derefString(req.Name))
	if name == "" {
		httputil.JSONError(w, http.StatusBadRequest, "a name is required — the extension's row, its directory and its project all take it from there")
		return
	}
	description := strings.TrimSpace(derefString(req.Description))
	if description == "" {
		httputil.JSONError(w, http.StatusBadRequest, "say what the extension should do — the agent chooses its type from those words")
		return
	}
	// The task handler is optional: a build that cannot run a turn still creates the extension.
	if h.ext == nil || h.projects == nil {
		httputil.Fail(w, fmt.Errorf("the create path is not wired"), "create extension", "")
		return
	}

	// Before the first directory exists, so a refusal creates nothing.
	if err := h.ext.ReserveDevName(name); err != nil {
		httputil.Fail(w, err, "create extension", "")
		return
	}

	repo, _, err := h.ext.ScaffoldDevRepo(name)
	if err != nil {
		// Every scaffolder refusal is something the person can act on, so it is their sentence.
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Everything after this line has been created, so every failure below unwinds what was made.
	cleanup := func(cause error) {
		if leftover := h.ext.RemoveDevRepo(repo); leftover != "" {
			logging.For(logging.Extensions).Warnf("LEFTOVER FILES: %s", leftover)
		}
		httputil.Fail(w, cause, "create extension", "")
	}

	// The repository must hold one commit before the project row exists: a worktree is cut from HEAD,
	// and an empty repository leaves the first task nothing to branch from.
	if err := initialCommit(repo, name); err != nil {
		cleanup(err)
		return
	}

	networkIsolation := true
	project, code, msg := h.projects.createProjectFromRequest(types.CreateProjectRequest{
		Path:                    repo,
		Name:                    filepath.Base(repo),
		SandboxEnabled:          true,
		SandboxNetworkIsolation: &networkIsolation,
	})
	if project == nil {
		// The project path answered its own sentence; only the directory it refused is ours to undo.
		if leftover := h.ext.RemoveDevRepo(repo); leftover != "" {
			logging.For(logging.Extensions).Warnf("LEFTOVER FILES: %s", leftover)
		}
		httputil.JSONError(w, code, msg)
		return
	}

	row, err := h.ext.CreateDevRow(name, project.ID)
	if err != nil {
		h.rollbackProject(project.ID)
		cleanup(err)
		return
	}

	task := &types.Task{
		ProjectID: project.ID,
		Title:     name,
		Prompt:    devTaskPreamble + description,
		Status:    types.StatusBacklog,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	// No browser is open here to choose a model, and the start below refuses a row with no provider in
	// the log alone.
	applyTaskDefaults(task, h.configStore, nil, nil)
	if err := h.store.CreateTask(task); err != nil {
		h.rollbackDevRow(project.ID)
		h.rollbackProject(project.ID)
		cleanup(err)
		return
	}
	thread := &types.TaskThread{TaskID: task.ID, Name: "Main", IsActive: true, GitBranch: task.Branch, CreatedAt: task.CreatedAt}
	if err := h.store.CreateThread(thread); err != nil {
		h.rollbackTask(project.ID, task.ID)
		h.rollbackDevRow(project.ID)
		h.rollbackProject(project.ID)
		cleanup(err)
		return
	}
	task.CurrentThreadID = thread.ID
	if err := h.store.UpdateTask(task); err != nil {
		h.rollbackTask(project.ID, task.ID)
		h.rollbackDevRow(project.ID)
		h.rollbackProject(project.ID)
		cleanup(err)
		return
	}
	if err := h.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: task.ID, ThreadID: thread.ID, Role: "user",
		Content: task.Prompt, CreatedAt: task.CreatedAt,
	}); err != nil {
		h.rollbackTask(project.ID, task.ID)
		h.rollbackDevRow(project.ID)
		h.rollbackProject(project.ID)
		cleanup(err)
		return
	}

	// The first task starts through the same schedule a card's start button uses, so the board never
	// opens onto a dead row. A start that fails is reported, not unwound: the extension is complete and
	// the person can start the task themselves.
	out := startOutcome{}
	if h.tasks != nil {
		out = h.tasks.startUnderSchedule(project.ID, task, nil)
	}
	if out.selfStartErr != nil {
		logging.For(logging.Extensions).Warnf("ext=%s was created but its first task did not start: %v", row.ID, out.selfStartErr)
	}

	httputil.JSONResponse(w, http.StatusCreated, map[string]any{
		"project":    project,
		"extension":  h.ext.DevResponse(row),
		"task":       task,
		"repository": repo,
	})
}

// initialCommit makes the scaffolded folder a repository with one commit, authored by the identity
// the server was configured with.
func initialCommit(dir, name string) error {
	if out, err := git.Run(dir, "init", "-b", "main"); err != nil {
		return fmt.Errorf("the repository could not be created: %s", strings.TrimSpace(out))
	}
	// The target is refused when it already exists, so everything in this directory came from this call.
	out, err := git.Run(dir, "add", "--", ".")
	if err != nil {
		return fmt.Errorf("the scaffolded files could not be staged: %s", strings.TrimSpace(out))
	}
	if out, err := git.Run(dir, append(git.GitAuthorFlags(), "commit", "-m", "Create "+name)...); err != nil {
		return fmt.Errorf("the scaffolded files could not be committed: %s", strings.TrimSpace(out))
	}
	return nil
}

// rollbackDevRow takes away the row the create made. A row left behind claims the name.
func (h *ExtensionScaffoldHandler) rollbackDevRow(projectID string) {
	if leftover := h.ext.DeleteDevRows(projectID); leftover != "" {
		logging.For(logging.Extensions).Warnf("development rows from a failed create were left behind: %s", leftover)
	}
}

// rollbackProject deletes the project row created by a create that failed afterwards. Nothing else
// exists under it yet — the repository goes with the removal the caller already made.
func (h *ExtensionScaffoldHandler) rollbackProject(projectID string) {
	if err := h.store.DeleteProject(projectID); err != nil {
		logging.For(logging.Extensions).Warnf("project %s from a failed create could not be removed: %v", projectID, err)
	}
}

// rollbackTask deletes the task row created by a create that failed afterwards. It never ran, so there
// is no container and no worktree to tear down.
func (h *ExtensionScaffoldHandler) rollbackTask(projectID, taskID string) {
	if err := h.store.DeleteTask(projectID, taskID); err != nil {
		logging.For(logging.Extensions).Warnf("task %s from a failed create could not be removed: %v", taskID, err)
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
