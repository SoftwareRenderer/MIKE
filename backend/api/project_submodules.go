package api

// The reading behind the submodule surface, with its handlers in project.go: the `http` archgate rule counts a
// domain package's files that import net/http and refuses a new one, and nothing here needs the transport to
// decide what to answer. A fetch runs here only as a person's click — a turn asks for one through
// backend/exec/submodules.go. See docs/git-operations.md → Submodules.

import (
	"context"

	"mike/backend/git"
)

// taskSubmoduleRows reads the submodules a task's own commit names and how the person's checkout holds each
// one. A task that has never started has no worktree to read; its listing comes from the checkout that
// worktree would be built from, which is the reading a start would have made.
func (h *ProjectHandler) taskSubmoduleRows(ctx context.Context, taskID, projectPath string) ([]git.SubmoduleState, error) {
	reading := projectPath
	if path, err := h.worktreeMgr.WorktreePath(taskID); err == nil && git.HasGitDir(path) {
		reading = path
	}
	return git.SubmoduleStates(ctx, reading, projectPath, h.submoduleFetch)
}

// initProjectSubmodules is the click: fetch into the person's own checkout what that checkout's commit records
// and does not hold — the whole checkout rather than the paths one task happened to need, because that is the
// state a click asks for.
//
// The three answers read differently on the notice and so stay apart: what came down, what git said about it,
// and what stayed missing — an unreachable upstream, a person's own checkout left in place, or an address MIKE
// will not dial.
func (h *ProjectHandler) initProjectSubmodules(ctx context.Context, projectPath string) (submoduleInit, error) {
	var init submoduleInit
	populated, err := git.InitSubmodules(ctx, projectPath, h.submoduleFetch, func(line string) {
		init.Reported = append(init.Reported, line)
	})
	if err != nil {
		return init, err
	}
	init.Populated = populated
	missing, err := git.CheckoutSubmoduleStates(ctx, projectPath, h.submoduleFetch)
	if err != nil {
		return init, err
	}
	for _, s := range missing {
		if !s.Populated {
			init.Missing = append(init.Missing, s)
		}
	}
	return init, nil
}

// submoduleInit is one click's answer, as the person's card reads it.
type submoduleInit struct {
	Populated []string
	Reported  []string
	Missing   []git.SubmoduleState
}

// Answer builds the response body, an empty list being an empty array so the notice distinguishes "this
// project has no submodules" from a request that answered nothing.
func (i submoduleInit) Answer() map[string]any {
	if i.Populated == nil {
		i.Populated = []string{}
	}
	if i.Reported == nil {
		i.Reported = []string{}
	}
	if i.Missing == nil {
		i.Missing = []git.SubmoduleState{}
	}
	return map[string]any{
		"populated": i.Populated,
		"reported":  i.Reported,
		"missing":   i.Missing,
	}
}

// submodulesAnswer builds a listing's body, an empty listing being an empty array for the same reason.
func submodulesAnswer(states []git.SubmoduleState) map[string]any {
	if states == nil {
		states = []git.SubmoduleState{}
	}
	return map[string]any{"submodules": states}
}
