package exec

// Whether a task may reach the network for a submodule is decided here, beside the turn that would make it,
// because the two answers that decide it are the project's: sandbox_network_isolation says whether anything
// this project runs ever leaves the machine, and auto_fetch_submodules says whether MIKE may contact a
// submodule URL nobody asked for. The git layer enforces what a fetch may name and where its address may be;
// this file decides whether one runs at all. See docs/git-operations.md → Submodules.

import (
	"context"
	"errors"
	"strings"
	"time"

	"mike/backend/git"
	"mike/backend/types"
	"mike/pkg/logging"
)

// submoduleAutoFetchTimeout bounds one task's whole submodule fetch, every level of it. No person is watching
// a turn's clock, and a repository whose remote hangs must not hold a task open.
const submoduleAutoFetchTimeout = 10 * time.Minute

// prepareSubmodules gives the task's worktree the submodules its commit names, fetching first for the ones the
// person's checkout never populated where the project's two switches allow it.
//
// Anything still unpopulated at the end stops the task on *git.SubmodulesUninitialized, which carries the
// address each submodule would be fetched from: the row goes to Review with that answer in it, and the card
// turns it into a notice with the button. A task is never left running on an empty submodule directory.
func (te *TaskExecutor) prepareSubmodules(setup *taskExecSetup) error {
	project := setup.project
	fetch := te.submoduleFetch

	unpopulated, err := te.unpopulatedSubmodules(setup, fetch)
	if err != nil || len(unpopulated) == 0 {
		return err
	}
	// Isolation is asked first: it refuses a fetch of any kind, whatever the row says about fetching.
	if project.SandboxNetworkIsolation || !project.AutoFetchSubmodules {
		return &git.SubmodulesUninitialized{Project: project.Path, Submodules: unpopulated}
	}
	if err := te.fetchSubmodules(setup, fetch, unpopulated); err != nil {
		return err
	}
	// Re-read rather than remember: a fetch that reported success and left the copy where git will not read
	// it is still a task that cannot start.
	unpopulated, err = te.unpopulatedSubmodules(setup, fetch)
	if err != nil || len(unpopulated) == 0 {
		return err
	}
	return &git.SubmodulesUninitialized{Project: project.Path, Submodules: unpopulated}
}

// unpopulatedSubmodules asks the git layer what this task's own commit names and the person's checkout does
// not hold. The rows carry the address and MIKE's own reason, so the notice and the refusal say the same thing.
func (te *TaskExecutor) unpopulatedSubmodules(setup *taskExecSetup, fetch git.SubmoduleFetch) ([]git.SubmoduleState, error) {
	states, err := git.SubmoduleStates(setup.ctx, setup.worktreePath, setup.project.Path, fetch)
	if err != nil {
		return nil, err
	}
	var out []git.SubmoduleState
	for _, s := range states {
		if !s.Populated {
			out = append(out, s)
		}
	}
	return out, nil
}

// fetchSubmodules asks git for the missing paths, and only those the project's own checkout also records.
func (te *TaskExecutor) fetchSubmodules(setup *taskExecSetup, fetch git.SubmoduleFetch, want []git.SubmoduleState) error {
	paths := make([]string, 0, len(want))
	for _, s := range want {
		paths = append(paths, s.Path)
	}
	ctx, cancel := context.WithTimeout(setup.ctx, submoduleAutoFetchTimeout)
	defer cancel()
	log := logging.For(logging.Git).With(logging.TaskID, setup.task.ID).With(logging.ProjectID, setup.projectID)

	te.reportSubmodule(setup, "Initializing submodules")
	populated, err := te.submoduleFetcher(ctx, setup.project.Path, paths, fetch, func(line string) {
		log.Warnf("%s", line)
		te.reportSubmodule(setup, line)
	})
	if len(populated) > 0 {
		log.Infof("fetched %d submodule(s) for the task: %s", len(populated), strings.Join(populated, ", "))
	}
	return err
}

// reportSubmodule puts one fetch outcome where the task's activity line already shows.
func (te *TaskExecutor) reportSubmodule(setup *taskExecSetup, activity string) {
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", activity))
}

// allowStoreFetch answers whether a task may answer a stale submodule pointer with one fetch into the
// person's shared store. It is the same pair of switches as the task's own fetch, because that fetch dials
// the store's recorded remote from the server process: consent to reach the network for a submodule covers
// both, and a project that granted neither gets no outbound connection from a turn at all.
func allowStoreFetch(p *types.Project) bool {
	return !p.SandboxNetworkIsolation && p.AutoFetchSubmodules
}

// submoduleFailCode is the code a failed sandbox setup is reported under. An uninitialized submodule gets its
// own because the card answers it with a button rather than a retry; everything else keeps the step it came
// from.
func submoduleFailCode(err error) string {
	var needInit *git.SubmodulesUninitialized
	if errors.As(err, &needInit) {
		return "submodules_uninitialized"
	}
	return "sandbox_setup_failed"
}
